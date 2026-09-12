#!/usr/bin/python3
"""Bounded AT-SPI service. Runs as echo; only the authenticated agent proxies it.

Each request owns its socket. Closing it cancels traversal/waits before the next
AT-SPI call. Individual D-Bus calls also have a short libatspi timeout. Actions
are never replayed after execution begins, even if the caller disconnects.
"""
import collections
import hashlib
import json
import os
import select
import socket
import subprocess
import time
import uuid

import gi
gi.require_version("Atspi", "2.0")
from gi.repository import Atspi, GLib

SOCKET = "/run/echo/accessibility/control.sock"
SESSION = str(uuid.uuid4())
REFERENCES = collections.OrderedDict()
SNAPSHOTS = collections.OrderedDict()
REQUESTS = {}
CHECKPOINT = lambda: None


class Failure(Exception):
    def __init__(self, code, message):
        super().__init__(message)
        self.code = code


def active(connection, deadline):
    if time.monotonic() > deadline:
        raise Failure("ui_timeout", "Accessibility request exceeded its deadline")
    if select.select([connection], [], [], 0)[0] and not connection.recv(1, socket.MSG_PEEK):
        raise Failure("ui_canceled", "UI caller disconnected; observe before continuing")
    context = GLib.MainContext.default()
    for _ in range(8):
        if not context.pending():
            break
        context.iteration(False)


def role(node):
    CHECKPOINT()
    return node.get_role_name().replace(" ", "_")


def name(node):
    CHECKPOINT()
    return (node.get_name() or "")[:600]


def states(node):
    CHECKPOINT()
    values = node.get_state_set()
    has = lambda state: values.contains(state)
    result = {"visible": has(Atspi.StateType.SHOWING) and has(Atspi.StateType.VISIBLE),
              "enabled": has(Atspi.StateType.ENABLED) and has(Atspi.StateType.SENSITIVE),
              "focused": has(Atspi.StateType.FOCUSED), "checked": has(Atspi.StateType.CHECKED),
              "selected": has(Atspi.StateType.SELECTED), "editable": has(Atspi.StateType.EDITABLE),
              "defunct": has(Atspi.StateType.DEFUNCT)}
    if result["defunct"]:
        return result
    if node.get_role() != Atspi.Role.PASSWORD_TEXT:
        text = node.get_text_iface()
        if text:
            # Accessible also has a deprecated zero-argument get_text accessor;
            # dispatch explicitly through Text to avoid GI's method collision.
            result["text"] = Atspi.Text.get_text(text, 0, min(32768, Atspi.Text.get_character_count(text)))
            if result["editable"]:
                result["value"] = result["text"]
    return result


def bounds(node):
    CHECKPOINT()
    component = node.get_component_iface()
    if not component:
        return None
    rectangle = component.get_extents(Atspi.CoordType.SCREEN)
    if rectangle.width <= 0 or rectangle.height <= 0:
        return None
    return {"x": rectangle.x, "y": rectangle.y, "width": rectangle.width, "height": rectangle.height}


def identity(node):
    ancestors = []
    parent = node.get_parent()
    for _ in range(12):
        CHECKPOINT()
        if not parent:
            break
        ancestors.append((role(parent), name(parent)))
        parent = parent.get_parent()
    return (node.get_process_id(), role(node), name(node), tuple(ancestors))


def action_names(node):
    action = node.get_action_iface()
    return [action.get_action_name(i) for i in range(action.get_n_actions())] if action else []


def resolve(ref, connection, deadline):
    active(connection, deadline)
    saved = REFERENCES.get(ref)
    if not saved or time.monotonic() - saved[2] > 600:
        raise Failure("ui_stale_target", "Native target expired. Observe again.")
    node, fingerprint, _ = saved
    if states(node)["defunct"] or identity(node) != fingerprint:
        raise Failure("ui_target_changed", "Native object or ancestor context changed. Observe again.")
    return node


def window_nodes(connection, deadline):
    desktop = Atspi.get_desktop(0)
    windows = []
    for app_index in range(min(desktop.get_child_count(), 256)):
        active(connection, deadline)
        app = desktop.get_child_at_index(app_index)
        if not app:
            continue
        for index in range(min(app.get_child_count(), 256)):
            active(connection, deadline)
            window = app.get_child_at_index(index)
            if window:
                windows.append(window)
    return windows


def remember(node):
    fingerprint = identity(node)
    # Holding the GI proxy preserves bus/object identity. Indexes in the tree
    # are used only during observation, never to rediscover an action target.
    ref = SESSION + "/" + str(uuid.uuid4())
    REFERENCES[ref] = (node, fingerprint, time.monotonic())
    while len(REFERENCES) > 4096:
        REFERENCES.popitem(last=False)
    return ref, fingerprint


def observe(params, connection, deadline):
    surfaces = []
    windows = window_nodes(connection, deadline)
    for window in windows:
        try:
            ref, _ = remember(window)
            surfaces.append({"id": ref, "kind": "desktop", "title": name(window),
                             "application": name(window.get_application()), "pid": window.get_process_id(), "bounds": bounds(window), "epoch": SESSION})
        except GLib.Error:
            continue
    surface_id = params.get("surfaceId", "desktop")
    surface = {"kind": "desktop", "id": surface_id, "epoch": SESSION}
    if params.get("list"):
        return {"surfaces": surfaces, "surface": surface, "targets": [], "capabilities": ["native-accessibility", "desktop-visual"]}
    if params.get("cursor"):
        snapshot_id, offset = params["cursor"].rsplit(":", 1)
        saved = SNAPSHOTS.get(snapshot_id)
        if not saved or time.monotonic() - saved[0] > 60 or saved[1] != surface_id:
            raise Failure("ui_stale_observation", "Native pagination expired. Observe again.")
        targets, truncated = saved[2:]
        offset = int(offset)
    else:
        root_ref = params.get("scopeRef") or (surface_id if surface_id != "desktop" else None)
        roots = [resolve(root_ref, connection, deadline)] if root_ref else windows
        queue = collections.deque((node, [], 0) for node in roots)
        seen, targets = set(), []
        visited = 0
        while queue and visited < 10000:
            active(connection, deadline)
            node, context, depth = queue.popleft()
            if node in seen:
                continue
            seen.add(node)
            visited += 1
            try:
                node_states = states(node)
                if node_states["defunct"]:
                    continue
                node_role, node_name = role(node), name(node)
                label = f"{node_role} {node_name}".strip()
                if not params.get("search") or params["search"].lower() in " ".join(context + [label]).lower():
                    actions = action_names(node)
                    targets.append({"_node": node, "role": node_role, "name": node_name, "states": node_states,
                                    "context": context[-5:], "bounds": bounds(node), "nativeActions": actions,
                                    "actions": (["click", "check"] if actions else []) + (["fill"] if node_states["editable"] else []) +
                                    (["focus", "press"] if node.get_component_iface() else []) + (["select"] if node.get_selection_iface() else [])})
                if depth < 40:
                    for index in range(min(node.get_child_count(), 10000 - visited)):
                        active(connection, deadline)
                        child = node.get_child_at_index(index)
                        if child:
                            queue.append((child, context + [label], depth + 1))
            except GLib.Error:
                continue
        truncated = bool(queue)
        snapshot_id, offset = str(uuid.uuid4()), 0
        SNAPSHOTS[snapshot_id] = (time.monotonic(), surface_id, targets, truncated)
        while len(SNAPSHOTS) > 8:
            SNAPSHOTS.popitem(last=False)
    limit = max(1, min(int(params.get("limit", 80)), 200))
    page = []
    for saved_target in targets[offset:offset + limit]:
        active(connection, deadline)
        target = dict(saved_target)
        node = target.pop("_node")
        if role(node) != target["role"] or name(node) != target["name"]:
            raise Failure("ui_stale_observation", "Native controls changed during pagination. Observe again.")
        target["ref"], _ = remember(node)
        target["states"] = states(node)
        target["bounds"] = bounds(node)
        page.append(target)
    return {"observationId": snapshot_id, "surface": surface, "timestamp": time.time(),
            "targets": page, "total": len(targets), "truncated": truncated,
            "nextCursor": f"{snapshot_id}:{offset + limit}" if offset + limit < len(targets) else None,
            "surfaces": surfaces, "capabilities": ["native-accessibility", "desktop-visual"]}


def verify(params, connection, deadline):
    expect = params.get("expect") or {}
    end = min(deadline, time.monotonic() + min(max(int(params.get("timeoutMs", 5000)), 1), 30000) / 1000)
    evidence = None
    while True:
        active(connection, deadline)
        node = resolve(expect.get("ref") or params.get("ref"), connection, deadline)
        evidence = states(node)
        kind = expect.get("kind")
        if kind == "visible":
            matched = evidence["visible"]
        elif kind == "hidden":
            matched = not evidence["visible"]
        elif kind in ("checked", "selected"):
            matched = evidence[kind] == expect.get("checked", True)
        elif kind in ("text", "value"):
            matched = evidence.get(kind) == expect.get("value")
        elif kind == "focused":
            matched = evidence["focused"]
        elif kind == "window":
            matched = name(node) == expect.get("value")
        else:
            raise Failure("ui_unsupported_predicate", "Native predicate is unsupported; use explicit visual verification if needed")
        if matched or time.monotonic() >= end:
            return {"status": "passed" if matched else "failed", "method": "deterministic", "evidence": evidence}
        select.select([connection], [], [], min(.1, end - time.monotonic()))


def xdotool(arguments, connection, deadline):
    active(connection, deadline)
    process = subprocess.Popen(["xdotool"] + arguments, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        while process.poll() is None:
            active(connection, deadline)
            select.select([connection], [], [], .02)
        if process.returncode:
            raise Failure("ui_physical_failed", "Physical input failed; observe before continuing")
    finally:
        if process.poll() is None:
            process.kill()
            process.wait()


def focused_at_point(x, y, connection, deadline):
    # Preserve selection/caret when typing into an already focused control. Also
    # require the active X11 window's PID, because GTK can retain internal focus
    # in a background window. Otherwise focus the requested point first.
    try:
        active_id = subprocess.check_output(["xdotool", "getactivewindow"], timeout=2).strip()
        active_pid = int(subprocess.check_output(["xdotool", "getwindowpid", active_id], timeout=2))
        for window in window_nodes(connection, deadline):
            if window.get_process_id() != active_pid:
                continue
            component = window.get_component_iface()
            if not component:
                continue
            node = component.get_accessible_at_point(round(x), round(y), Atspi.CoordType.SCREEN)
            for _ in range(20):
                active(connection, deadline)
                if not node:
                    break
                if states(node)["focused"]:
                    return True
                child_component = node.get_component_iface()
                child = child_component.get_accessible_at_point(round(x), round(y), Atspi.CoordType.SCREEN) if child_component else None
                if child == node:
                    break
                node = child
    except (GLib.Error, subprocess.SubprocessError, ValueError):
        pass
    return False


def act(params, connection, deadline):
    request_id = params.get("requestId")
    if not request_id:
        raise Failure("invalid_arguments", "UI actions require a request ID")
    signature = hashlib.sha256(json.dumps(params, sort_keys=True).encode()).hexdigest()
    if request_id in REQUESTS:
        prior = REQUESTS[request_id]
        if prior[0] != signature:
            raise Failure("ui_request_conflict", "Request ID already used with different arguments")
        return prior[1]
    if len(REQUESTS) >= 10000:
        raise Failure("ui_request_limit", "Runtime action journal is full; restart before more actions")
    result = {"execution": "not_started", "verification": {"status": "unverified", "method": "none"}, "backend": "native", "recovery": "none"}
    REQUESTS[request_id] = (signature, result)
    started = False
    try:
        action = params.get("action")
        if params.get("epoch") != SESSION:
            raise Failure("ui_stale_target", "Accessibility runtime restarted. Observe again.")
        if action == "point":
            x, y = params.get("x"), params.get("y")
            if not isinstance(x, (int, float)) or not isinstance(y, (int, float)) or x < 0 or y < 0:
                raise Failure("invalid_arguments", "Invalid desktop point")
            active(connection, deadline)
            physical = params.get("pointAction", "click")
            if physical not in ("click", "hover", "type", "press", "scroll", "drag"):
                raise Failure("ui_unsupported_action", "Unsupported physical action")
            if physical == "press" and not params.get("key"):
                raise Failure("invalid_arguments", "A keyboard key is required")
            width, height = map(int, subprocess.check_output(["xdotool", "getdisplaygeometry"], timeout=2).split())
            if x >= width or y >= height:
                raise Failure("invalid_arguments", "Point is outside the desktop display")
            if physical == "drag" and not (0 <= params.get("toX", -1) < width and 0 <= params.get("toY", -1) < height):
                raise Failure("invalid_arguments", "Drag destination is outside the desktop display")
            started = True
            xdotool(["mousemove", "--sync", str(round(x)), str(round(y))], connection, deadline)
            if physical == "drag":
                try:
                    xdotool(["mousedown", "1", "mousemove", "--sync", str(round(params["toX"])), str(round(params["toY"])), "mouseup", "1"], connection, deadline)
                finally:
                    subprocess.run(["xdotool", "mouseup", "1"], timeout=2, check=False)
            elif physical == "scroll":
                for delta, negative, positive in ((params.get("deltaY", 0), 4, 5), (params.get("deltaX", 0), 6, 7)):
                    if delta:
                        xdotool(["click", "--repeat", str(min(100, max(1, abs(int(delta)) // 100))), "--delay", "20", str(positive if delta > 0 else negative)], connection, deadline)
            elif physical != "hover":
                button = {"left": "1", "middle": "2", "right": "3"}.get(params.get("button", "left"), "1")
                if physical not in ("type", "press") or not focused_at_point(x, y, connection, deadline):
                    xdotool(["click", "--repeat", str(min(3, max(1, int(params.get("clickCount", 1))))), button], connection, deadline)
                if physical == "type":
                    xdotool(["type", "--clearmodifiers", "--", str(params.get("text", ""))], connection, deadline)
                elif physical == "press":
                    xdotool(["key", "--clearmodifiers", "--", str(params["key"])], connection, deadline)
        else:
            node = resolve(params.get("ref"), connection, deadline)
            state = states(node)
            if not state["visible"] or not state["enabled"]:
                raise Failure("ui_not_actionable", "Control is hidden or disabled")
            active(connection, deadline)
            if action in ("click", "check"):
                actions = action_names(node)
                index = next((i for i, value in enumerate(actions) if value.lower() in ("click", "activate", "press", "toggle", "invoke")), None)
                if index is None:
                    raise Failure("ui_unsupported_action", "Control exposes no activation action; use visual grounding")
                if action != "check" or state["checked"] != params.get("checked", True):
                    started = True
                    if not node.get_action_iface().do_action(index):
                        raise Failure("ui_native_failed", "Native action did not acknowledge completion")
            elif action == "fill":
                editable = node.get_editable_text_iface()
                if not editable or not state["editable"]:
                    raise Failure("ui_unsupported_action", "Control is not editable")
                started = True
                if not editable.set_text_contents(str(params.get("text", ""))):
                    raise Failure("ui_native_failed", "Native text edit failed")
            elif action in ("focus", "press"):
                component = node.get_component_iface()
                if not component:
                    raise Failure("ui_unsupported_action", "Control cannot receive focus")
                started = True
                if not component.grab_focus():
                    raise Failure("ui_native_failed", "Native focus request failed")
                if action == "press":
                    if not states(node)["focused"]:
                        raise Failure("ui_native_failed", "Control did not receive focus")
                    xdotool(["key", "--clearmodifiers", "--", str(params.get("key", ""))], connection, deadline)
            elif action == "select":
                child = resolve(params.get("toRef"), connection, deadline)
                selection = node.get_selection_iface()
                if not selection or child.get_parent() != node:
                    raise Failure("ui_unsupported_action", "Select requires an observed direct child of the selection container")
                started = True
                if not selection.select_child(child.get_index_in_parent()):
                    raise Failure("ui_native_failed", "Native selection failed")
            elif action == "scroll":
                raise Failure("ui_unsupported_action", "Relative native scrolling requires a visually located scroll region")
            else:
                raise Failure("ui_unsupported_action", "Native action unsupported; use visual grounding")
        result["execution"] = "completed"
        intrinsic = {"fill": {"kind": "value", "value": params.get("text", "")}, "check": {"kind": "checked", "checked": params.get("checked", True)},
                     "focus": {"kind": "focused"}, "select": {"kind": "selected", "ref": params.get("toRef")}}
        expect = params.get("expect") or intrinsic.get(action)
        if expect:
            try:
                result["verification"] = verify(dict(params, expect=expect, timeoutMs=params.get("verifyTimeoutMs", 5000)), connection, deadline)
            except Exception:
                result["verification"] = {"status": "unknown", "method": "deterministic"}
    except Exception as error:
        result["execution"] = "unknown" if started else "not_started"
        result["error"] = {"code": getattr(error, "code", "ui_native_failed"), "message": str(error)[:500]}
    return result


def serve():
    global CHECKPOINT
    Atspi.init()
    Atspi.set_timeout(500, 1000)
    os.makedirs(os.path.dirname(SOCKET), mode=0o700, exist_ok=True)
    if os.path.exists(SOCKET):
        os.unlink(SOCKET)
    server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    server.bind(SOCKET)
    os.chmod(SOCKET, 0o600)
    server.listen(8)
    while True:
        connection, _ = server.accept()
        with connection:
            try:
                connection.settimeout(5)
                data = bytearray()
                while b"\n" not in data:
                    chunk = connection.recv(8192)
                    if not chunk or len(data) + len(chunk) > 1024 * 1024:
                        raise Failure("invalid_arguments", "Request is missing or too large")
                    data.extend(chunk)
                request = json.loads(data.split(b"\n", 1)[0])
                connection.settimeout(None)
                deadline = time.monotonic() + 40
                CHECKPOINT = lambda: active(connection, deadline)
                method, params = request.get("method"), request.get("params", {})
                if method == "health":
                    result = {"ok": True, "epoch": SESSION, "capabilities": ["ui-v1", "native-accessibility"]}
                elif method == "ui_observe":
                    result = observe(params, connection, deadline)
                elif method == "ui_act":
                    result = act(params, connection, deadline)
                elif method == "ui_verify":
                    result = verify(params, connection, deadline)
                else:
                    raise Failure("ui_unsupported_action", "Unknown accessibility operation")
                response = {"ok": True, "data": result}
            except Exception as error:
                response = {"ok": False, "code": getattr(error, "code", "ui_accessibility_unavailable"), "error": str(error)[:500]}
            try:
                connection.sendall(json.dumps(response).encode() + b"\n")
            except (BrokenPipeError, ConnectionResetError):
                pass


if __name__ == "__main__":
    serve()
