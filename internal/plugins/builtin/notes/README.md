# Echo Notes

Notes is a built-in, optional Echo plugin for writing private Markdown notes scoped to the active workspace. It provides a full-page source editor, a safe local preview, title filtering, and debounced autosave.

Notes are stored in Echo's quota-limited, namespaced plugin storage. They are not written into the workspace as `.md` files, are not exposed to model tools, and remain retained when the plugin is uninstalled unless the owner explicitly removes retained plugin data in Settings.

Like every optional Echo plugin, Notes must be staged, reviewed, installed, and enabled by the owner before it can run.
