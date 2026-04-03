---
name: project_summary
description: "Summarize a project directory — show structure, git status, and key files"
risk: low
parameters:
  path:
    type: string
    description: "Absolute path to the project directory"
    required: true
---
## Steps

1. Show directory tree
```bash
find {{.path}} -maxdepth 3 -not -path '*/\.*' -not -path '*/node_modules/*' -not -path '*/vendor/*' | head -50
```

2. Check git status
```bash
cd {{.path}} && git status --short 2>/dev/null || echo "(not a git repo)"
```

3. Show README if exists
```bash
cat {{.path}}/README.md 2>/dev/null | head -30 || echo "(no README.md found)"
```
