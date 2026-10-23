//go:build !windows

package watchdog

// startAgent Unix 上直接拉起子进程，由 systemd/upstart 之外的方式保活。
func startAgent(binPath string, args []string) error {
	return spawnAgent(binPath, args)
}
