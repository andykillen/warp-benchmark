package main

// executeTargets waits for every target, even when one returns an error.
// Each callback owns its target's sequential stream.
func executeTargets(targets []Target, parallel bool, run func(Target) error) error {
	if !parallel {
		for _, target := range targets {
			if err := run(target); err != nil {
				return err
			}
		}
		return nil
	}
	results := make([]chan error, len(targets))
	for i, target := range targets {
		results[i] = make(chan error, 1)
		go func(t Target, result chan<- error) { result <- run(t) }(target, results[i])
	}
	var first error
	for _, result := range results {
		if err := <-result; err != nil && first == nil {
			first = err
		}
	}
	return first
}
