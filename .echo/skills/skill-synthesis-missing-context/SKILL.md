---
name: skill-synthesis-missing-context
description: Handles cases where the chat transcript is omitted or empty during skill synthesis.
triggers:
    - missing transcript
    - omitted context
    - empty chat log
---

# Missing Transcript Handling

No conversation transcript was provided for analysis. To generate a reusable workspace skill, supply a complete chat log containing project-specific decisions, architecture details, or implementation steps.
