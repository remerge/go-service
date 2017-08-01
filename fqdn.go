package service

import (
	"bytes"
	"os/exec"
)

// GetFQDN uses /bin/hostname to get the fqdn for the host
func GetFQDN() string {
	// #nosec
	out, err := exec.Command("/bin/hostname", "-f").Output()
	if err != nil {
		return "localhost"
	}
	return string(bytes.TrimSpace(out))
}
