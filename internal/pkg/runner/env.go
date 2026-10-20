package runner

import "os"

// osEnviron 包装 os.Environ，便于单测隔离。
func osEnviron() []string { return os.Environ() }
