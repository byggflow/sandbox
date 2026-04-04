package protocol

import "testing"

func TestOpConstants(t *testing.T) {
	ops := map[string]string{
		"OpFsRead":          OpFsRead,
		"OpFsWrite":         OpFsWrite,
		"OpFsList":          OpFsList,
		"OpFsStat":          OpFsStat,
		"OpFsRemove":        OpFsRemove,
		"OpFsRename":        OpFsRename,
		"OpFsMkdir":         OpFsMkdir,
		"OpFsUpload":        OpFsUpload,
		"OpFsDownload":      OpFsDownload,
		"OpProcessExec":     OpProcessExec,
		"OpProcessSpawn":    OpProcessSpawn,
		"OpProcessPty":      OpProcessPty,
		"OpProcessResize":   OpProcessResize,
		"OpEnvGet":          OpEnvGet,
		"OpEnvSet":          OpEnvSet,
		"OpEnvDelete":       OpEnvDelete,
		"OpEnvList":         OpEnvList,
		"OpNetFetch":        OpNetFetch,
		"OpTemplateSave":    OpTemplateSave,
		"OpSessionResumed":  OpSessionResumed,
		"OpSessionReplaced": OpSessionReplaced,
	}

	// Verify all ops are non-empty and follow "category.action" format.
	for name, op := range ops {
		if op == "" {
			t.Errorf("%s is empty", name)
			continue
		}
		hasDot := false
		for _, c := range op {
			if c == '.' {
				hasDot = true
				break
			}
		}
		if !hasDot {
			t.Errorf("%s = %q, want category.action format", name, op)
		}
	}

	// Verify uniqueness.
	seen := make(map[string]string)
	for name, op := range ops {
		if prev, ok := seen[op]; ok {
			t.Errorf("duplicate op %q: %s and %s", op, prev, name)
		}
		seen[op] = name
	}
}
