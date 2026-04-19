package tool

import "time"

// CurrentSitemapCache returns the process-wide sitemap cache injected at
// startup, if any.
func CurrentSitemapCache() SitemapCache {
	return currentSitemapCache()
}

// CurrentHumanDemoSink returns the active human-demo sink.
func CurrentHumanDemoSink() HumanDemoSink {
	return currentHumanDemoSink()
}

// CurrentHumanEventSourceFactory returns the active human-event source
// factory, if one has been configured.
func CurrentHumanEventSourceFactory() HumanEventSourceFactory {
	return currentEventSourceFactory()
}

// CurrentPatternFailureStore returns the active pattern-failure store.
func CurrentPatternFailureStore() PatternFailureStore {
	return currentFailureStore()
}

// ForceSharedPatternLibraryRefreshForTest clears the debounce window used by
// shared pattern-library refresh. Intended for deterministic tests only.
func ForceSharedPatternLibraryRefreshForTest() {
	patternLibMu.Lock()
	patternLibCheckedAt = time.Time{}
	lib := patternLib
	patternLibMu.Unlock()
	if lib != nil {
		lib.mu.Lock()
		lib.lastReloadCheck = time.Time{}
		lib.mu.Unlock()
	}
}
