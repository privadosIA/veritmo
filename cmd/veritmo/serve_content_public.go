//go:build !private

package main

// contentOption lets a build take an option out of the arguments of `serve` before the flag
// set parses them. The public build has none: every argument stays, so an option it does not
// know is refused by the flag set like any other (exit status 2).
func contentOption(args []string) ([]string, bool) { return args, false }
