# i18n Keys Addition - Learnings

## What Worked Well

1. **Clean slate approach**: When the JSON files became corrupted with duplicate entries, the best solution was to completely rewrite both files from scratch rather than trying to fix the existing structure.

2. **Validation before build**: Using Node.js to validate JSON syntax before attempting the build helped catch issues early.

3. **Systematic key organization**: Grouping new keys by section (recordings, detail, cameras, etc.) made it easier to ensure all required keys were added consistently.

4. **Simultaneous updates**: Adding keys to both en.json and zh.json at the same time prevented key mismatches.

## Issues Encountered

1. **JSON corruption**: The editing process introduced duplicate entries and incorrect indentation, leading to JSON parsing errors:
   - Lines like `"recordings.notPinned": "Not Pinned"` appeared twice with different indentation
   - Mixed indentation levels (2-space vs 4-space)

2. **Build failure**: The corrupted JSON files caused the Vite build to fail with syntax errors, preventing progress verification.

3. **Key validation complexity**: Without `jq` available, had to use Node.js to compare key counts and matches.

## Solutions Applied

1. **Backup strategy**: Created backups before major changes
2. **Complete rewrite**: Instead of fixing corrupted files, created new clean versions with all keys
3. **Systematic verification**: Used Node.js to:
   - Validate JSON syntax
   - Count keys
   - Verify key matches between files
   - Confirm build success

## Key Patterns Identified

1. **File size impact**: Clean JSON files were 259 lines each (vs 270+ with duplicates)
2. **Key count**: 252 keys total in each language file
3. **Build success**: All warnings are Svelte compiler hints, not actual errors
4. **Translation consistency**: All English keys have corresponding Chinese translations

## Best Practices for Future i18n Updates

1. **Always validate JSON** after editing - use Node.js require() or equivalent
2. **Add keys in batches** by section to maintain organization
3. **Verify key symmetry** between language files immediately
4. **Test build early** to catch issues before accumulating many changes
5. **Use proper indentation** consistently (2 spaces for this project)