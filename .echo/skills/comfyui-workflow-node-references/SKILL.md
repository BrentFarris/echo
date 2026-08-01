---
name: comfyui-workflow-node-references
description: ComfyUI workflow JSON node reference format and common pitfalls when workflows fail with shape errors.
triggers:
    - comfyui workflow
    - node reference
    - ComfyUI error
    - shape attribute
---

## ComfyUI Workflow Node References

Node connections in ComfyUI workflows must use **array format** `["node_id", output_index]`, never string format `"node_id_output"`.

### Correct (array format)
```json
"clip": ["2", 0],
"model": ["1", 0],
"images": ["8", 0]
```

### Incorrect (string format — causes errors)
```json
"clip": "2_0",
"model": "1_0",
"images": "8_0"
```

### Symptoms of wrong format
- `AttributeError: 'str' object has no attribute 'shape'` on SaveImage or other nodes
- The error occurs because ComfyUI expects the reference to be resolved as a list, not parsed as a string

### Template variables
Workflow files can use template variables like `{{PROMPT}}`, `{{NEGATIVE_PROMPT}}`, `{{MODEL}}`, `{{SEED}}`, `{{STEPS}}`, `{{CFG_SCALE}}`, `{{WIDTH}}`, `{{HEIGHT}}` — these are substituted by the Go tool before sending to ComfyUI.

### Location
Custom workflows live in `.comfy/workflows/`. Built-in workflows are in `internal/comfyui/workflows/comfyui/`.
