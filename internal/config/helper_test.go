// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package config

import (
	"os"
)

func writeFile(p, s string, mode os.FileMode) error {
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		return err
	}
	return os.Chmod(p, mode)
}
