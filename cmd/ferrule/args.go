package main

import "flag"

// parseWithSubject parses flags that may appear on either side of a positional argument.
//
// Go's flag package stops at the first non-flag argument, so `ferrule add anthropic
// -base-url http://…` parses zero flags and silently leaves -base-url in the tail. The
// flag is not rejected; it simply does nothing, and the command proceeds with defaults.
// For -base-url that means the key goes to the provider's own endpoint instead of the
// one the person named — a flag that lies about where a credential is being sent.
//
// The positionals are lifted out first, then the remaining flags are parsed, so order
// stops mattering.
func parseWithSubject(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional, flags []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			// A flag that takes a value consumes the next argument, unless it was
			// written as -name=value.
			if !hasInlineValue(a) && takesValue(fs, a) && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}
		positional = append(positional, a)
	}
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	return positional, nil
}

func hasInlineValue(a string) bool {
	for i := range a {
		if a[i] == '=' {
			return true
		}
	}
	return false
}

// takesValue reports whether a flag expects a following argument. Booleans do not.
func takesValue(fs *flag.FlagSet, a string) bool {
	name := a
	for len(name) > 0 && name[0] == '-' {
		name = name[1:]
	}
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !(ok && bf.IsBoolFlag())
}
