package tools

// BaseDirTool is implemented by tools whose relative paths can be anchored to a
// session working directory.
type BaseDirTool interface {
	Tool
	WithBaseDir(baseDir string) Tool
}

func (t PatchTool) WithBaseDir(baseDir string) Tool {
	if t.BaseDir == "" {
		t.BaseDir = baseDir
	}
	return t
}

func (t EditTool) WithBaseDir(baseDir string) Tool {
	if t.BaseDir == "" {
		t.BaseDir = baseDir
	}
	return t
}

func (t ReadTool) WithBaseDir(baseDir string) Tool {
	if t.BaseDir == "" {
		t.BaseDir = baseDir
	}
	return t
}

func (t WriteTool) WithBaseDir(baseDir string) Tool {
	if t.BaseDir == "" {
		t.BaseDir = baseDir
	}
	return t
}

func (t DeleteTool) WithBaseDir(baseDir string) Tool {
	if t.BaseDir == "" {
		t.BaseDir = baseDir
	}
	return t
}

func (t GrepTool) WithBaseDir(baseDir string) Tool {
	if t.BaseDir == "" {
		t.BaseDir = baseDir
	}
	return t
}

func (t GlobTool) WithBaseDir(baseDir string) Tool {
	if t.BaseDir == "" {
		t.BaseDir = baseDir
	}
	return t
}

func (t HandoffTool) WithBaseDir(baseDir string) Tool {
	if t.BaseDir == "" {
		t.BaseDir = baseDir
	}
	return t
}

func (t ShellTool) WithBaseDir(baseDir string) Tool {
	if t.BaseDir == "" {
		t.BaseDir = baseDir
	}
	return t
}

func (t SpawnTool) WithBaseDir(baseDir string) Tool {
	if t.BaseDir == "" {
		t.BaseDir = baseDir
	}
	return t
}

func (t LspTool) WithBaseDir(baseDir string) Tool {
	if t.BaseDir == "" {
		t.BaseDir = baseDir
	}
	return t
}

func (t *ViewImageTool) WithBaseDir(baseDir string) Tool {
	if t.BaseDir == "" {
		t.BaseDir = baseDir
	}
	return t
}
