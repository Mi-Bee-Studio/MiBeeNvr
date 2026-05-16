## Learnings

## Decisions

## Issues

## Problems

## Task 1: Go Module Rename (completed)

- Module path: `github.com/mickey/camvault` → `github.com/Mi-Bee-Studio/MiBeeNvr`
- 20 `.go` files had import paths replaced
- Directory renamed: `cmd/camvault/` → `cmd/mibee-nvr/`
- Intentionally preserved: `camvault.yaml` (config flag default), `camvault.db` (DB filename)
- Test temp dirs updated for consistency: `/tmp/camvault-test-*` → `/tmp/mibee-nvr-test-*`
- MQTT test topic prefix updated: `"camvault"` → `"mibee-nvr"` in client_test.go
- Integration test package renamed: `camvault_tests` → `mibee_nvr_tests`
- FTP Name() driver method: `"camvault"` → `"mibee-nvr"`
- All 82 tests pass, go vet clean, zero old import paths remain
Task 3 completed successfully: All frontend dependencies merged from root to web/, root artifacts removed, and web build passes.
TASK 3 COMPLETION SUMMARY: Merged frontend dependencies from root package.json to web/, removed root frontend artifacts, and verified web build succeeds.

## Task 2: Directory Restructure (completed)

- internal/types/ → internal/model/: Cross-cutting domain types (Recorder, StorageProvider, Camera, Recording, Segment, etc.) used by 16 files across 8 packages
- Decision: `internal/model/` chosen over merging into any single package (types had no single natural home — used by camera, recorder, storage, api, cleanup, upload, ftp, integration tests)
- When moving Go packages: must update BOTH import paths AND package qualifier references (types.XXX → model.XXX)
- go-mp4/ was a leftover vendor copy — go.mod already had the dependency, safe to delete
- tests/integration_test.go: 577 lines, 7 real tests — kept at root (Go convention allows root _test.go)
- .gitkeep cleanup: remove from dirs that now have real content (deploy/, tests/), keep in empty dirs (web_embed/)
Task 4 completed successfully. All acceptance criteria met.
Task 4 completed successfully. All acceptance criteria met.
Task 4 completed successfully. All acceptance criteria met.
Task 4 completed successfully. All acceptance criteria met.
Task 4 completed successfully. All acceptance criteria met.

## Task 6: UI CSS Beautification (2026-04-27)

### Design System Migration: Amber to Cyan/Blue/Teal

**Key Changes:**
- Replaced all amber/orange references with cyan/blue/teal palette
- Primary accent: cyan-500/cyan-400
- Secondary: blue-500/blue-400
- Focus rings updated to cyan-500
- Gradient buttons: from-cyan-600 to-blue-600

**Component Polish:**
- Cards: Added subtle borders (border-slate-700/60) and transparency (bg-slate-800/90)
- Buttons: Added gradient and hover shadows
- Stat cards: Increased font size from text-3xl to text-4xl (mlsbs.top style)
- Added backdrop-blur-sm for modern glass effect

### Patterns for Dark Tech Theme (mlsbs.top style)

**Typography:**
- Large metric numbers: text-4xl or text-5xl for emphasis
- Clean sans-serif system fonts
- Tight tracking for headings (tracking-tight)

**Color Usage:**
- Dominant: Deep slate grays (slate-900, slate-800)
- Accent: Cyan/Blue for actions and highlights (NOT harsh)
- Semantic: Emerald (success), Red (error), Yellow (warning)

**Spatial Composition:**
- Subtle borders with transparency: border-slate-700/50
- Controlled gradients: bg-gradient-to-br from-slate-800 to-slate-800/80
- Professional spacing: 4px grid system
- Generous padding on cards: p-6

### Build Troubleshooting Lessons

**Issue:** Multiple Svelte compilation errors after CSS edits
**Root Cause:** Accidentally modified HTML structure during CSS class updates
**Examples:**
- Duplicated opening div tags when updating card classes
- Removed or misplaced closing tags when replacing content blocks
- Left unclosed container divs after restructuring

**Prevention:**
1. When using Edit tool with multiple edits, ensure non-overlapping ranges
2. After each edit, verify the structure hasn't broken
3. Run build frequently to catch errors early
4. Use line-by-line replacement for structural changes, not bulk replacement

**Debug Commands:**
```bash
# Count opening and closing divs
grep -c "<div" Stats.svelte
grep -c "</div>" Stats.svelte

# List all div tags with line numbers
cat Stats.svelte | grep -n "<div\|</div>"

# Check conditional blocks
grep -n "{#\|{/if\|{:else" Stats.svelte
```

### Design System Best Practices

**Before making visual changes:**
1. READ the entire design system first (app.css)
2. READ at least 5-10 existing components
3. Understand naming conventions and spacing system
4. Identify all places where the color/style is used

**When updating colors:**
1. Update the design system (CSS variables, component classes) FIRST
2. Then update component files
3. Search for all uses of the old color (grep)
4. Replace systematically

**Component Polish:**
- Subtle > Dramatic (avoid excessive decorations)
- Consistent borders and shadows across all cards
- Use existing spacing scale (don't invent new values)
- Match existing hover states and transitions
