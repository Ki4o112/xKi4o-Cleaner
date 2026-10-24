package main

// A new startup entry, added while nobody looked (a setup, or something
// worse), is told to the user. What was there when the app first looked is
// known and never reported. Free of Windows calls.

import "sort"

// newStartupKeys: the keys in now that known does not have. first: known is
// empty (the first look), nothing is new then.
func newStartupKeys(known map[string]bool, now []string) []string {
	if len(known) == 0 {
		return nil
	}
	var out []string
	for _, k := range now {
		if !known[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}
