// Codebridge
// SPDX-License-Identifier: AGPL-3.0-or-later

package agent

import "context"

type filesystemModule struct {
	builtInModulePolicy
	runtime *Runtime
}

func newFilesystemModule(runtime *Runtime) ToolModule { return &filesystemModule{runtime: runtime} }
func (*filesystemModule) Name() string                { return "filesystem" }
func (*filesystemModule) Specs() []ToolSpec           { return filesystemToolSpecs() }
func (m *filesystemModule) Handle(ctx context.Context, _ CallIdentity, tool string, args map[string]any) (any, error) {
	return m.runtime.handleFS(ctx, tool, args)
}
func (*filesystemModule) Health(context.Context) any {
	return healthyModule("filesystem", len(filesystemToolSpecs()))
}
func (*filesystemModule) Close() error { return nil }

func filesystemToolSpecs() []ToolSpec {
	return []ToolSpec{
		roSpec("list_files", "List files", "List files and directories under a root.", object(map[string]any{"path": str("Directory."), "recursive": boolean(), "limit": integer()})),
		roSpec("read_file", "Read file", "Read one UTF-8 file with optional line ranges.", object(map[string]any{"path": str("File path."), "start_line": integer(), "line_count": integer(), "max_chars": integer()}, "path")),
		roSpec("stat_path", "Stat path", "Return file or directory metadata.", object(map[string]any{"path": str("Path.")}, "path")),
		roSpec("search_text", "Search text", "Search workspace text with ripgrep or a Go fallback.", object(map[string]any{"query": str("Text or regex."), "path": str("Search root."), "regex": boolean(), "glob": str("File glob."), "context": integer(), "limit": integer()}, "query")),
		roSpec("find_files", "Find files", "Find file paths matching a glob.", object(map[string]any{"glob": str("Name glob."), "path": str("Search root."), "limit": integer()}, "glob")),
		roSpec("read_many", "Read many", "Read up to 100 files or line ranges in one call.", object(map[string]any{"paths": array(str("")), "requests": array(object(nil)), "max_chars_per_file": integer(), "concurrency": integer()})),
		roSpec("repo_overview", "Repo overview", "Return a compact tree and manifest list.", object(map[string]any{"path": str("Root."), "depth": integer(), "max_entries": integer()})),
		rwSpec("write_file", "Write file", "Create or overwrite a UTF-8 file.", object(map[string]any{"path": str("File path."), "content": str("File content.")}, "path", "content"), false),
		rwSpec("replace_in_file", "Replace in file", "Replace exact text in one file.", object(map[string]any{"path": str("File path."), "old_text": str("Exact old text."), "new_text": str("Replacement."), "replace_all": boolean()}, "path", "old_text"), false),
		rwSpec("apply_patch", "Apply patch", "Apply a unified diff or structured operations.", object(map[string]any{"diff": str("Unified diff."), "operations": array(object(nil))}), true),
		rwSpec("make_dir", "Make directory", "Create a directory recursively.", object(map[string]any{"path": str("Directory path.")}, "path"), false),
		rwSpec("move_path", "Move path", "Move or rename a file or directory.", object(map[string]any{"from": str("Source."), "to": str("Destination.")}, "from", "to"), true),
		rwSpec("delete_path", "Delete path", "Delete a file or directory inside roots.", object(map[string]any{"path": str("Target."), "recursive": boolean()}, "path"), true),
		roSpec("preview_patch", "Preview patch", "Dry-run a diff or structured operations.", object(map[string]any{"diff": str("Unified diff."), "operations": array(object(nil))})),
		roSpec("validate_patch", "Validate patch", "Return patch conflicts without writing.", object(map[string]any{"diff": str("Unified diff."), "operations": array(object(nil))})),
		rwSpec("undo_last_patch", "Undo last patch", "Restore the most recent backup batch.", object(nil), true),
	}
}
