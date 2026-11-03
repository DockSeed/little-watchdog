// SPDX-FileCopyrightText: the little-watchdog contributors
// SPDX-License-Identifier: MIT

package testfake

import "testing"

func TestCheckHTML(t *testing.T) {
	for _, s := range []string{"", "plain", "<b>a</b> &amp; <code>x &lt;y&gt;</code>", "<b>a <code>b</code></b>"} {
		if err := CheckHTML(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"<b>a", "a</b>", "<b><code></b></code>", "a < b", "a > b", "a & b", "<i>x</i>", "&nbsp;", "<b", "&#x;"} {
		if CheckHTML(s) == nil {
			t.Errorf("%q must be rejected", s)
		}
	}
}
