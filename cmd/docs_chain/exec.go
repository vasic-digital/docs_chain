package main

import (
	"fmt"
	"os"
	"os/exec"
)

// lookPath resolves a bare tool name on PATH (doctor tool-availability check).
func lookPath(tool string) (string, error) {
	return exec.LookPath(tool)
}

// resolveExec resolves an exec transform binary: a bare name is looked up on
// PATH; a path-bearing reference must exist and be executable.
func resolveExec(bin string) (string, error) {
	if bin == "" {
		return "", fmt.Errorf("empty exec")
	}
	if !containsSep(bin) {
		return exec.LookPath(bin)
	}
	fi, err := os.Stat(bin)
	if err != nil {
		return "", err
	}
	if fi.IsDir() || fi.Mode()&0o111 == 0 {
		return "", fmt.Errorf("%s is not an executable file", bin)
	}
	return bin, nil
}

func containsSep(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == os.PathSeparator {
			return true
		}
	}
	return false
}
