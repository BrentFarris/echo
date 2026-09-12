import gi
import json
gi.require_version("Gtk", "3.0")
from gi.repository import Gtk

window = Gtk.Window(title="Echo UI Fixture")
window.set_default_size(400, 300)
window.connect("destroy", Gtk.main_quit)
box = Gtk.Box(orientation=Gtk.Orientation.VERTICAL, spacing=10)
box.set_border_width(20)
window.add(box)
entry = Gtk.Entry()
entry.get_accessible().set_name("Document title")
box.pack_start(entry, False, False, 0)
check = Gtk.CheckButton(label="Ready")
box.pack_start(check, False, False, 0)
status = Gtk.Label(label="Unsaved")
count = 0

def save(button):
    global count
    count += 1
    with open("/tmp/echo-ui-clicks", "w") as stream:
        stream.write(str(count))
    with open("/tmp/echo-ui-result.json", "w") as stream:
        json.dump({"count": count, "value": entry.get_text(), "checked": check.get_active()}, stream)
    status.set_text("Saved once" if count == 1 else "Duplicate submission")

button = Gtk.Button(label="Save document")
button.connect("clicked", save)
box.pack_start(button, False, False, 0)
disabled = Gtk.Button(label="Unavailable action")
disabled.set_sensitive(False)
box.pack_start(disabled, False, False, 0)
box.pack_start(status, False, False, 0)
window.show_all()
Gtk.main()
