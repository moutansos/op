package tmux

import "github.com/GianlucaP106/gotmux/gotmux"

// gotmuxIntegrationBoundary keeps the pinned adapter dependency compile-checked.
// v0.5.0 has no context-aware execution API, so deadline-sensitive production
// paths use the raw compatibility layer instead of constructing this type.
type gotmuxIntegrationBoundary = gotmux.Tmux
