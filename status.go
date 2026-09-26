// status.go - foreground process tracking and container/context detection.
//
// For each pane we poll the foreground process group of its PTY every second
// by reading /proc/<shellPid>/stat (field tpgid = terminal foreground PGID),
// then reading /proc/<tpgid>/comm for the process name.
//
// Container context is detected at pane startup by reading the shell process's
// /proc/<pid>/environ.  Toolbox, Distrobox, Podman, and LXD each leave
// distinctive markers in the environment or filesystem.
//
// The status badge is rendered last in each render pass (on top of everything
// else) in the top-right corner of each pane.  It shows:
//
//	⬡ <container-name>  - Toolbox / Podman container
//	▣ <container-name>  - Distrobox container
//	ssh / sudo / su / root - notable foreground process
package main

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
)

// podmanNameCache maps hex container IDs to friendly names so that
// `podman inspect` is only spawned once per container across all panes.
var podmanNameCache sync.Map // key: string containerID, value: string name

// ---------------------------------------------------------------------------
// /proc helpers
// ---------------------------------------------------------------------------

// termFgPGID returns the foreground process group ID (tpgid) of the terminal
// controlling the given process.  It parses field 8 of /proc/<pid>/stat.
// Returns 0 on any error.
func termFgPGID(pid int) int {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0
	}
	s := string(data)
	// The comm field is wrapped in parentheses and may itself contain parens/
	// spaces; find the LAST ')' so we always skip the full comm field.
	rp := strings.LastIndex(s, ")")
	if rp < 0 {
		return 0
	}
	// After ')': state ppid pgrp session tty_nr tpgid ...
	fields := strings.Fields(s[rp+1:])
	if len(fields) < 6 {
		return 0
	}
	pgid, err := strconv.Atoi(fields[5]) // index 5 = tpgid
	if err != nil || pgid <= 0 {
		return 0
	}
	return pgid
}

// procComm returns the name of the process with the given PID by reading
// /proc/<pid>/comm.  Returns "" on error.
func procComm(pid int) string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// fgProcessCmdline returns the full argv of the foreground process group
// of the shell with the given PID.  Used by Alt+F1 to clone ssh/sudo sessions.
func fgProcessCmdline(shellPid int) []string {
	pgid := termFgPGID(shellPid)
	if pgid <= 0 {
		return nil
	}
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pgid))
	if err != nil || len(data) == 0 {
		return nil
	}
	raw := strings.TrimRight(string(data), "\x00")
	return strings.Split(raw, "\x00")
}

// sshHostFromCmdline parses the remote hostname from an ssh/mosh argv slice.
// It skips the program name and any flag arguments (those starting with "-"
// and their values), then returns the first bare argument — the destination.
// The user@ prefix is stripped so only the hostname is returned.
func sshHostFromCmdline(args []string) string {
	// Flags that consume the next argument as a value.
	valueFlags := map[string]bool{
		"-b": true, "-c": true, "-D": true, "-e": true, "-E": true,
		"-F": true, "-i": true, "-J": true, "-l": true, "-L": true,
		"-m": true, "-o": true, "-p": true, "-Q": true, "-R": true,
		"-S": true, "-w": true, "-W": true,
	}
	skip := false
	for _, a := range args[1:] {
		if skip {
			skip = false
			continue
		}
		if strings.HasPrefix(a, "-") {
			if valueFlags[a] {
				skip = true
			}
			continue
		}
		// First non-flag arg is user@host or host.
		if idx := strings.Index(a, "@"); idx >= 0 {
			return a[idx+1:]
		}
		return a
	}
	return ""
}

// whose image matches imageName.  Returns the first container ID found, or "".
// Used as a fallback when process-tree walking cannot find the container.
func findContainerByAncestor(imageName, containerType string) string {
	ct := containerType
	if ct == "" {
		ct = "podman"
	}
	out, err := exec.Command(ct, "ps", "-q", "--filter", "ancestor="+imageName, "--format", "{{.ID}}").Output()
	if err != nil || len(out) == 0 {
		// docker uses a slightly different --format syntax; try without it
		out, err = exec.Command(ct, "ps", "-q", "--filter", "ancestor="+imageName).Output()
		if err != nil || len(out) == 0 {
			return ""
		}
	}
	// take the first ID on the first line
	line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	return line
}

// findRunningContainerFromPID walks the child process tree rooted at pid
// looking for a process whose cgroup matches a libpod container (podman/docker).
// Returns the container ID (hex) on success, "" on failure.
// Used to resolve the actual container ID when "podman run" is the foreground
// process — in that case the image name is known but not the container ID.
func findRunningContainerFromPID(pid int) string {
	var walk func(pid int, depth int) string
	walk = func(pid int, depth int) string {
		if depth > 6 || pid <= 1 {
			return ""
		}
		if ct, cid := detectFromCgroup(pid); ct == "podman" && cid != "" {
			return cid
		}
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/task/%d/children", pid, pid))
		if err != nil {
			return ""
		}
		for _, s := range strings.Fields(string(data)) {
			child, _ := strconv.Atoi(s)
			if child > 1 {
				if id := walk(child, depth+1); id != "" {
					return id
				}
			}
		}
		return ""
	}
	return walk(pid, 0)
}

// an lxc/incus exec ancestor.  Returns the full argv of that process, which
// includes the container name and the command that was used to enter it
// (e.g. ["lxc", "exec", "xx", "--", "su", "--login", "jsn"]).
// Returns nil if no such ancestor is found within 20 levels.
func findLXCAncestorCmdline(pid int) []string {
	visited := map[int]bool{}
	current := pid
	for depth := 0; depth < 20; depth++ {
		if visited[current] || current <= 1 {
			break
		}
		visited[current] = true

		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", current))
		if err == nil && len(data) > 0 {
			args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
			if len(args) > 0 {
				bin := args[0]
				if idx := strings.LastIndexByte(bin, '/'); idx >= 0 {
					bin = bin[idx+1:]
				}
				if (bin == "lxc" || bin == "incus") && len(args) > 1 && args[1] == "exec" {
					return args
				}
			}
		}

		statusData, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", current))
		if err != nil {
			break
		}
		ppid := 0
		for _, line := range strings.Split(string(statusData), "\n") {
			if strings.HasPrefix(line, "PPid:") {
				if v, err := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:"))); err == nil {
					ppid = v
				}
				break
			}
		}
		if ppid <= 1 {
			break
		}
		current = ppid
	}
	return nil
}

// detectFromEnvSlice checks a slice of "KEY=VALUE" strings for known
// container markers.  Returns "toolbox", "distrobox", "podman", or "".
func detectFromEnvSlice(env []string) string {
	for _, entry := range env {
		switch {
		case strings.HasPrefix(entry, "TOOLBOX_PATH="):
			return "toolbox"
		case strings.HasPrefix(entry, "DISTROBOX_ENTER_PATH="):
			return "distrobox"
		case entry == "container=podman" || strings.HasPrefix(entry, "container=podman"):
			return "podman"
		}
	}
	return ""
}

// detectContainerFromPID returns the container type for the environment of the
// given PID.  It uses three sources in order:
//
//  1. os.Environ() of the running bunk process.  The child shell inherits
//     its environment from bunk via cmd.Env, so this is always correct for
//     the initial pane and any split that spawns a bare shell.
//
//  2. /proc/<pid>/environ - for cases where the child might differ (e.g. an
//     async OSC 176 callback firing for a process spawned differently).
//     This may fail with EACCES if the caller is not the process owner.
//
//  3. Filesystem markers that are world-readable:
//     - /run/.containerenv  - Podman creates this 0-byte file in every container
//     - /dev/lxd/sock       - LXD mounts this guest API socket into every container
//     - /proc/1/cgroup      - world-readable; cgroupsv1 paths contain "/lxc/"
//     - /proc/1/environ     - only readable as root; tried last
func detectContainerFromPID(pid int) string {
	// 1. Own environment (most reliable, no permission issues).
	if ct := detectFromEnvSlice(os.Environ()); ct != "" {
		return ct
	}

	// 2. Target process /proc environ.
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid)); err == nil {
		if ct := detectFromEnvSlice(strings.Split(string(data), "\x00")); ct != "" {
			return ct
		}
	}

	// 3b. Podman fallback: /run/.containerenv is created by Podman in every
	//     container, world-readable, even when container=podman is not
	//     propagated to the child process.
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return "podman"
	}

	// 3c. LXD/LXC/Incus detection (multiple methods, all non-root-friendly).
	// Method 1: guest API socket - LXD mounts /dev/lxd/sock, Incus /dev/incus/sock.
	if _, err := os.Stat("/dev/lxd/sock"); err == nil {
		return "lxd"
	}
	if _, err := os.Stat("/dev/incus/sock"); err == nil {
		return "lxd"
	}
	// Method 2: /run/systemd/container - systemd inside an LXD container
	//           writes "lxc" here; world-readable.
	if data, err := os.ReadFile("/run/systemd/container"); err == nil {
		if strings.TrimSpace(string(data)) == "lxc" {
			return "lxd"
		}
	}
	// Method 3: /proc/1/cgroup - world-readable; cgroupsv1 paths have "/lxc/".
	if cg, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		if strings.Contains(string(cg), "/lxc/") {
			return "lxd"
		}
	}
	// Method 4: /proc/1/environ - only readable as root.
	if pid != 1 {
		if init1, err := os.ReadFile("/proc/1/environ"); err == nil {
			for _, entry := range strings.Split(string(init1), "\x00") {
				if entry == "container=lxc" {
					return "lxd"
				}
			}
		}
	}

	return ""
}

// lxdContainerName returns the LXD container name using two methods:
//
//  1. cgroupsv1: parse "/lxc/<name>/" from /proc/1/cgroup (world-readable).
//  2. Fallback:  hostname — LXD sets the container hostname to its name.
func lxdContainerName() string {
	if cg, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		for _, line := range strings.Split(string(cg), "\n") {
			if idx := strings.Index(line, "/lxc/"); idx >= 0 {
				rest := line[idx+5:] // skip "/lxc/"
				if end := strings.IndexByte(rest, '/'); end > 0 {
					return rest[:end]
				}
				if name := strings.TrimSpace(rest); name != "" {
					return name
				}
			}
		}
	}
	if name, err := os.Hostname(); err == nil {
		return name
	}
	return ""
}

// containerSpawnArgs returns the argv for spawning `shell` inside the named
// container.  Returns nil for an unrecognised containerType or for "lxd"
// (LXD/LXC) where bunk is already running inside the container and new
// panes will naturally inherit the container context without special wrapping.
func containerSpawnArgs(containerID, containerType, shell string) []string {
	switch containerType {
	case "toolbox":
		return []string{"toolbox", "run", "--container", containerID, shell}
	case "distrobox":
		return []string{"distrobox", "enter", "-n", containerID, "--", shell}
	case "podman":
		return []string{"podman", "exec", "-it", containerID, shell}
	case "lxc", "incus":
		return []string{containerType, "exec", containerID, "--", shell}
	case "lxd":
		// bunk is running inside the LXD container; child panes inherit
		// the container context automatically.  No wrapper needed.
		return nil
	}
	return nil
}

// isRunningContainer checks whether containerID refers to a running container
// that we can exec into.  Returns false for image names (from "podman run")
// or stopped containers.
func isRunningContainer(containerID, containerType string) bool {
	switch containerType {
	case "podman", "docker":
		out, err := exec.Command(containerType, "inspect", "--format", "{{.State.Running}}", containerID).Output()
		return err == nil && strings.TrimSpace(string(out)) == "true"
	case "toolbox":
		out, err := exec.Command("podman", "inspect", "--format", "{{.State.Running}}", containerID).Output()
		return err == nil && strings.TrimSpace(string(out)) == "true"
	case "distrobox":
		out, err := exec.Command("podman", "inspect", "--format", "{{.State.Running}}", containerID).Output()
		return err == nil && strings.TrimSpace(string(out)) == "true"
	case "lxc", "incus":
		out, err := exec.Command(containerType, "info", containerID).Output()
		return err == nil && strings.Contains(string(out), "Status: RUNNING")
	}
	return true // unknown types: assume OK
}

// ---------------------------------------------------------------------------
// Goroutine: poll foreground process
// ---------------------------------------------------------------------------

// detectFromCgroup reads /proc/<pid>/cgroup (world-readable) and checks for
// Podman's libpod cgroup path pattern.  This works even when the process is
// owned by a different UID (e.g. rootless Podman with user namespaces), where
// /proc/<pid>/environ would return EACCES.
//
// Returns ("podman", containerID) if a libpod cgroup is found, ("","") otherwise.
// The containerID is the full hex container ID, not the friendly name.
func detectFromCgroup(pid int) (ctype, containerID string) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return "", ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		// cgroupsv2: "0::<path>"
		// cgroupsv1: "<n>:<subsystem>:<path>"
		parts := strings.SplitN(line, ":", 3)
		if len(parts) < 3 {
			continue
		}
		path := parts[2]

		// Podman: libpod_payload-<id> or libpod-<id>
		for _, sep := range []string{"libpod_payload-", "libpod-"} {
			if idx := strings.Index(path, sep); idx >= 0 {
				rest := path[idx+len(sep):]
				end := strings.IndexAny(rest, "./\n")
				if end > 0 {
					return "podman", rest[:end]
				}
				if id := strings.TrimSpace(rest); id != "" {
					return "podman", id
				}
			}
		}

		// LXD/LXC: cgroup path contains "/lxc/<name>/"
		if idx := strings.Index(path, "/lxc/"); idx >= 0 {
			rest := path[idx+5:] // skip "/lxc/"
			end := strings.IndexAny(rest, "/\n")
			if end > 0 {
				return "lxd", rest[:end]
			}
			if name := strings.TrimSpace(rest); name != "" {
				return "lxd", name
			}
		}
	}
	return "", ""
}

// podmanContainerName resolves a Podman container ID to its friendly name by
// running "podman inspect".  Returns the ID itself (first 12 chars) on failure.
func podmanContainerName(containerID string) string {
	if containerID == "" {
		return ""
	}
	if v, ok := podmanNameCache.Load(containerID); ok {
		return v.(string)
	}
	out, err := exec.Command("podman", "inspect", "--format", "{{.Name}}", containerID).Output()
	var name string
	if err != nil || len(out) == 0 {
		if len(containerID) > 12 {
			name = containerID[:12]
		} else {
			name = containerID
		}
	} else {
		name = strings.TrimSpace(string(out))
	}
	podmanNameCache.Store(containerID, name)
	return name
}

// detectContainerInfoFromProcEnv returns the
// container type and container name for that specific process.
// Unlike detectContainerFromPID it does NOT check os.Environ() or filesystem
// markers — it only looks at the given process's own environment.
//
// If /proc/<pid>/environ is unreadable (e.g. rootless Podman user namespaces
// where container processes appear as sub-UIDs), falls back to cgroup-based
// detection, which is always world-readable.
//
// If cgroup detection also fails, tries detectExecSession to handle the case
// where the foreground process is a container runtime CLI (podman exec, docker
// exec, kubectl exec) whose own environ is on the host, not inside the
// container.
func detectContainerInfoFromProcEnv(pid int) (ctype, cname string) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err == nil {
		for _, entry := range strings.Split(string(data), "\x00") {
			switch {
			case strings.HasPrefix(entry, "TOOLBOX_PATH="):
				ctype = "toolbox"
			case strings.HasPrefix(entry, "DISTROBOX_ENTER_PATH="):
				ctype = "distrobox"
			case entry == "container=podman" || strings.HasPrefix(entry, "container=podman"):
				ctype = "podman"
			case strings.HasPrefix(entry, "HOSTNAME="):
				cname = entry[9:]
			case strings.HasPrefix(entry, "CONTAINER_ID="):
				if cname == "" {
					cname = entry[13:]
				}
			}
		}
		if ctype != "" {
			L.Debug("detectContainerInfoFromProcEnv: found via environ", "pid", pid, "type", ctype, "name", cname)
			return
		}
	}

	// Cgroup fallback: /proc/<pid>/cgroup is world-readable.
	// Rootless Podman puts container processes under a libpod cgroup path
	// even when their UID differs (making environ unreadable by bunk).
	if ct, cid := detectFromCgroup(pid); ct != "" {
		ctype = ct
		// Resolve the hex container ID to a friendly name via `podman inspect`.
		// Called only when the foreground process changes (~1s polling), so
		// the ~30ms subprocess cost is acceptable.
		cname = podmanContainerName(cid)
		L.Debug("detectContainerInfoFromProcEnv: found via cgroup", "pid", pid, "type", ctype, "name", cname)
		return
	}

	// CLI exec session fallback: handles "podman exec <name>", "docker exec",
	// "kubectl exec" — the CLI process runs on the HOST so its environ and
	// cgroup won't show container markers; we parse the cmdline instead.
	if ct, cn := detectExecSession(pid); ct != "" {
		L.Debug("detectContainerInfoFromProcEnv: found via exec cmdline", "pid", pid, "type", ct, "name", cn)
		return ct, cn
	}

	return
}

// detectExecSession detects when the foreground process is a container runtime
// CLI executing a command inside a container (podman exec, docker exec,
// kubectl exec / oc exec).  It parses /proc/<pid>/cmdline to extract the
// subcommand and container/pod name.
//
// Layout of the relevant subcommands:
//
//	podman exec  [options] CONTAINER COMMAND [ARG...]
//	docker exec  [options] CONTAINER COMMAND [ARG...]
//	kubectl exec [options] POD [-c CONTAINER] -- COMMAND [ARG...]
//	oc exec      [options] POD [-c CONTAINER] -- COMMAND [ARG...]
//	lxc exec     CONTAINER -- COMMAND [ARG...]
func detectExecSession(pid int) (ctype, cname string) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(data) == 0 {
		return "", ""
	}
	// cmdline is NUL-separated; drop trailing NUL.
	args := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	if len(args) < 2 {
		return "", ""
	}

	bin := args[0]
	// Trim path prefix: "/usr/bin/podman" → "podman"
	if idx := strings.LastIndexByte(bin, '/'); idx >= 0 {
		bin = bin[idx+1:]
	}

	// Find the subcommand (first non-flag arg).
	subcmdIdx := -1
	for i := 1; i < len(args); i++ {
		if args[i] != "" && !strings.HasPrefix(args[i], "-") {
			subcmdIdx = i
			break
		}
	}
	if subcmdIdx < 0 {
		return "", ""
	}
	subcmd := args[subcmdIdx]
	rest := args[subcmdIdx+1:]

	switch bin {
	case "podman", "docker":
		if subcmd != "exec" && subcmd != "run" {
			return "", ""
		}
		// First positional arg after flags is the container name.
		name := firstPositionalCLIArg(rest)
		if name == "" {
			return "", ""
		}
		return bin, name

	case "kubectl", "oc":
		if subcmd != "exec" {
			return "", ""
		}
		// kubectl exec [options] POD [-c CONTAINER] -- cmd
		// First positional arg is the pod name.
		name := firstPositionalCLIArg(rest)
		if name == "" {
			return "", ""
		}
		return "kubectl", name

	case "lxc", "incus":
		// lxc exec CONTAINER -- COMMAND [ARG...]
		// incus exec CONTAINER -- COMMAND [ARG...]
		if subcmd != "exec" && subcmd != "shell" {
			return "", ""
		}
		// After "exec", first positional arg is the container name.
		name := firstPositionalCLIArg(rest)
		if name == "" {
			return "", ""
		}
		return bin, name // "lxc" or "incus" as type
	}

	return "", ""
}

// firstPositionalCLIArg returns the first positional (non-flag) argument from
// a CLI arg list, skipping flags and their values.
//
// For flags that take a value (e.g. --user root, -e VAR=val) we use a simple
// heuristic: long flags (--foo) without "=" skip the next arg; short flags
// (-f) that are not in a known set of boolean flags also skip the next arg.
// This handles the common cases without needing per-runtime flag tables.
func firstPositionalCLIArg(args []string) string {
	// Boolean short flags common to podman/docker exec; don't consume next arg.
	boolShort := map[string]bool{
		"-i": true, "-t": true, "-d": true,
		"--interactive": true, "--tty": true, "--detach": true, "--privileged": true,
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "" || arg == "--" {
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			return arg // positional found
		}
		if boolShort[arg] {
			continue // boolean flag, no value
		}
		if strings.Contains(arg, "=") {
			continue // --flag=value form, value already consumed
		}
		// Assume flag takes a separate value → skip next arg.
		i++
	}
	return ""
}

// trackFgProcess polls the foreground process group of this pane's PTY once
// per second.  When the process name or container context changes it updates
// the Pane fields and signals the render loop to repaint.
// Stops when done is closed or the pane dies.
func (p *Pane) trackFgProcess(redraw chan struct{}, done chan struct{}) {
	if p.cmd.Process == nil {
		return
	}
	shellPid := p.cmd.Process.Pid
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	var lastName, lastCT, lastCN, lastSSHHost string
	var lastPGID int // cached to skip expensive /proc reads when unchanged
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
		}

		p.mu.Lock()
		dead := p.dead
		baseCT := p.baseContainerType
		baseCN := p.baseContainerID
		p.mu.Unlock()
		if dead {
			return
		}

		name := ""
		var pgid int
		if pgid = termFgPGID(shellPid); pgid > 0 {
			name = procComm(pgid)
		}

		// A short-lived app can negotiate and exit between polls, leaving
		// the same shell PGID as before. Check ownership on every tick.
		p.mu.Lock()
		if len(p.kittyStack) > 0 && p.kittyOwnerPGID != pgid {
			p.clearStaleKittyMode(termFgPGID(shellPid))
		}
		p.mu.Unlock()

		// Only re-run the expensive detection (/proc/environ, cgroup, podman
		// inspect) when the foreground PGID actually changes.  For most ticks
		// the foreground process is idle at the shell prompt, so this skips
		// virtually all costly reads.
		ct, cn, sshHost := lastCT, lastCN, lastSSHHost
		if pgid != lastPGID {
			lastPGID = pgid
			p.foregroundChanged(pgid <= 0 || pgid == shellPid, name, time.Now())
			ct, cn = "", ""
			sshHost = ""

			if pgid > 0 {
				ct, cn = detectContainerInfoFromProcEnv(pgid)
				L.Debug("trackFgProcess: detection (pgid changed)", "pane", p.id, "pgid", pgid, "name", name, "ct", ct, "cn", cn)
			}
			if ct == "" {
				ct, cn = baseCT, baseCN
			}
			if (name == "ssh" || name == "mosh") && pgid > 0 {
				data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pgid))
				if err == nil && len(data) > 0 {
					raw := strings.TrimRight(string(data), "\x00")
					sshHost = sshHostFromCmdline(strings.Split(raw, "\x00"))
				}
			}
		}

		if name == lastName && ct == lastCT && cn == lastCN && sshHost == lastSSHHost {
			continue
		}
		L.Debug("trackFgProcess: status change", "pane", p.id, "name", name, "container_type", ct, "container_id", cn, "ssh_host", sshHost)
		lastName, lastCT, lastCN, lastSSHHost = name, ct, cn, sshHost

		p.mu.Lock()
		p.fgProcess = name
		p.containerType = ct
		p.containerID = cn
		p.sshHost = sshHost
		p.mu.Unlock()

		select {
		case redraw <- struct{}{}:
		default:
		}
	}
}

// clearStaleKittyMode requires p.mu.
func (p *Pane) clearStaleKittyMode(pgid int) {
	if pgid > 0 && p.kittyOwnerPGID > 0 && pgid != p.kittyOwnerPGID {
		p.kittyStack = p.kittyStack[:0]
		p.kittyOwnerPGID = 0
	}
}

// ---------------------------------------------------------------------------
// Status badge rendering
// ---------------------------------------------------------------------------

// drawAllPaneStatus renders the status badge for every pane in the subtree.
// Must be called AFTER all other drawing so the badge paints on top.
func drawAllPaneStatus(scr tcell.Screen, n *Node, active *Pane, rt resolvedTheme, zoomed bool) {
	if n.isLeaf() {
		drawPaneStatus(scr, n.pane, n.pane == active, rt, zoomed)
		return
	}
	drawAllPaneStatus(scr, n.left, active, rt, zoomed)
	drawAllPaneStatus(scr, n.right, active, rt, zoomed)
}

// statusOverlayKeyLocked returns a stable signature for the badge content that
// overlays the pane's top row. renderPane uses this to force a repaint when
// status badges change even if vt10x itself has not marked any rows dirty.
//
// p.mu must be held by the caller.
func statusOverlayKeyLocked(p *Pane, now time.Time) string {
	tempActive := !p.statusMsgEnd.IsZero() && now.Before(p.statusMsgEnd) && p.statusMsg != ""
	return fmt.Sprintf("%t|%t|%s|%s|%s|%s|%s",
		p.passthrough,
		tempActive,
		p.statusMsg,
		p.fgProcess,
		p.containerType,
		p.containerID,
		p.sshHost,
	)
}

// paneBadge is one status badge: text and colours derived from the theme.
type paneBadge struct {
	text   string
	fg, bg tcell.Color
}

// paneBadges lists pane p's status badges in display order, left to right;
// the last one is the most important (drawn closest to the edge):
//
//	[ COPIED ]         - temporary flash message
//	[ ZOOM ]           - pane is zoomed
//	[⬡ my-toolbox]     - container / ssh / sudo context
//	[-N]               - scrollback line count
//	[ PASS ]           - keyboard passthrough
//
// Shared by the TUI (drawPaneStatus) and the GUI.
func paneBadges(p *Pane, rt resolvedTheme, zoomed bool) []paneBadge {
	p.mu.Lock()
	fgProc := p.fgProcess
	containerID := p.containerID
	containerType := p.containerType
	sbOff := p.sbOff
	tempMsg := p.statusMsg
	tempActive := !p.statusMsgEnd.IsZero() && time.Now().Before(p.statusMsgEnd)
	sshHost := p.sshHost
	passthrough := p.passthrough
	p.mu.Unlock()

	// Badge colors derived from the theme palette so they adapt automatically.
	// See the palette index table in config.go (ThemeDef).
	// Backgrounds are blended 40% toward the theme bg to soften intensity.
	dim := func(c tcell.Color) tcell.Color {
		r1, g1, b1 := c.RGB()
		r2, g2, b2 := rt.bg.RGB()
		mix := func(a, b int32) int32 { return (a*60 + b*40) / 100 }
		return tcell.NewRGBColor(mix(r1, r2), mix(g1, g2), mix(b1, b2))
	}
	badgeText := rt.palette[7]       // white — badge foreground
	colorRed := dim(rt.palette[1])   // sudo / su
	colorGreen := dim(rt.palette[2]) // success (COPIED)
	colorYellow := rt.palette[3]     // scroll count foreground (not dimmed)
	colorBlue := dim(rt.palette[4])  // ssh
	colorCyan := dim(rt.palette[6])  // container
	colorDark := rt.palette[0]       // scroll count background

	var badges []paneBadge
	if tempActive && tempMsg != "" {
		badges = append(badges, paneBadge{" " + tempMsg + " ", badgeText, colorGreen})
	}
	if zoomed {
		badges = append(badges, paneBadge{" ZOOM ", colorYellow, colorDark})
	}
	if text, kind := paneContext(fgProc, containerType, containerID, sshHost); text != "" {
		bg := colorDark
		switch kind {
		case "sudo":
			bg = colorRed
		case "ssh":
			bg = colorBlue
		case "container":
			bg = colorCyan
		}
		badges = append(badges, paneBadge{" " + text + " ", badgeText, bg})
	}
	if sbOff > 0 {
		badges = append(badges, paneBadge{fmt.Sprintf(" -%d ", sbOff), colorYellow, colorDark})
	}
	if passthrough {
		badges = append(badges, paneBadge{" PASS ", badgeText, colorCyan})
	}
	return badges
}

// paneContext describes where a pane's shell runs: container, SSH host,
// or elevated shell, e.g. "⬡ mybox · ⇄ host". kind is the one that colours
// it ("sudo" over "ssh" over "container"); both are "" on the plain host.
func paneContext(fgProc, containerType, containerID, sshHost string) (text, kind string) {
	var parts []string
	if containerType != "" {
		icon := "⬡"
		if containerType == "distrobox" {
			icon = "▣"
		}
		label := containerType
		if containerID != "" {
			label = containerID
		}
		parts = append(parts, icon+" "+label)
		kind = "container"
	}
	switch fgProc {
	case "ssh", "mosh":
		label := "⇄"
		if sshHost != "" {
			label += " " + sshHost
		} else {
			label += " " + fgProc
		}
		parts = append(parts, label)
		if fgProc == "ssh" {
			kind = "ssh"
		}
	case "sudo", "su":
		parts = append(parts, fgProc)
		kind = "sudo"
	}
	return strings.Join(parts, " · "), kind
}

// context is paneContext for a pane; p.mu must not be held.
func (p *Pane) context() (text, kind string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return paneContext(p.fgProcess, p.containerType, p.containerID, p.sshHost)
}

// drawPaneStatus draws pane p's badges in its top-right corner (TUI).
func drawPaneStatus(scr tcell.Screen, p *Pane, _ bool, rt resolvedTheme, zoomed bool) {
	badges := paneBadges(p, rt, zoomed)
	if len(badges) == 0 {
		return
	}
	p.mu.Lock()
	px, py, pw := p.x, p.y, p.w
	sbOff := p.sbOff
	p.mu.Unlock()

	// Draw right-to-left: last badge in the slice goes closest to the edge.
	rightEdge := px + pw
	if sbOff > 0 {
		rightEdge-- // reserve scrollbar column
	}
	for i := len(badges) - 1; i >= 0; i-- {
		runes := []rune(badges[i].text)
		w := len(runes)
		startX := rightEdge - w
		if startX < px {
			break // no more room
		}
		style := tcell.StyleDefault.Foreground(badges[i].fg).Background(badges[i].bg).Bold(true)
		for j, ch := range runes {
			scr.SetContent(startX+j, py, ch, nil, style)
		}
		rightEdge = startX
	}
}
