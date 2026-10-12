package shell

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// procs are the processes of this user, as far as their files can be read.
func procs() []proc {
	dirs, _ := filepath.Glob("/proc/[0-9]*")
	uid := os.Getuid()
	var table []proc
	for _, d := range dirs {
		info, err := os.Stat(d)
		if err != nil || ownerOf(info) != uid {
			continue
		}
		data, err := os.ReadFile(filepath.Join(d, "stat"))
		if err != nil {
			continue
		}
		// pid (comm) state ppid pgrp …: the name may hold spaces and
		// parentheses, so the fields are counted after its last one.
		text := string(data)
		end := strings.LastIndexByte(text, ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(text[end+1:])
		if len(fields) < 20 {
			continue
		}
		pid, _ := strconv.Atoi(filepath.Base(d))
		ppid, _ := strconv.Atoi(fields[1])
		pgid, _ := strconv.Atoi(fields[2])
		start, _ := strconv.ParseInt(fields[19], 10, 64)
		table = append(table, proc{pid, ppid, pgid, start, fields[0] == "Z"})
	}
	return table
}

// tagged are the processes of this user with the tag of a service in their
// environment.
func tagged(tag string) []int {
	var pids []int
	for _, p := range procs() {
		if p.pid == self || p.zombie {
			continue
		}
		if env, err := os.ReadFile("/proc/" + strconv.Itoa(p.pid) + "/environ"); err == nil && hasTag(env, tag) {
			pids = append(pids, p.pid)
		}
	}
	return pids
}

// ownerOf is the user that owns a file.
func ownerOf(info os.FileInfo) int {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return int(st.Uid)
	}
	return -1
}

// hasTag reports whether env, the environment of a process as variables
// that each end with a zero byte, holds the tag of a service.
func hasTag(env []byte, tag string) bool {
	want := []byte(serviceEnv + "=" + tag)
	for v := range bytes.SplitSeq(env, []byte{0}) {
		if bytes.Equal(v, want) {
			return true
		}
	}
	return false
}
