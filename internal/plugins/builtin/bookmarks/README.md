# Echo Bookmarks

Install and enable Bookmarks in Settings → Plugins. Its icon opens a file-grouped
Code sidebar. Toggle the current line with Ctrl+K, K (also Ctrl+K, Ctrl+K), or
Command on macOS. The command palette also provides Bookmarks: Toggle.

Hover or focus a bookmark to rename or remove it. Empty names restore the source
line label. Clicking a bookmark focuses its line in the editor. Markers follow
edits, undo, and redo in Echo; deleting the marked text leaves the marker at the
nearest surviving line. File and folder moves in Echo preserve bookmarks.
External edits to closed files are not tracked.

Bookmarks are stored privately per workspace on the Echo server, not in source
files. Uninstall retains this data until it is explicitly removed in Settings.
This plugin uses a host-rendered Code view reserved for built-in Bookmarks;
third-party packages cannot contribute native Code sidebars or editor access.
