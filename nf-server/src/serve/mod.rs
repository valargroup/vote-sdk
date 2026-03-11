//! PIR HTTP server modules.
//!
//! Split from the monolithic `cmd_serve.rs` into:
//! - [`state`] — shared types (`AppState`, `ServingState`, `ServerPhase`)
//! - [`rebuild`] — snapshot rebuild pipeline and management endpoints
//! - [`handlers`] — PIR data, query, health, and root endpoints

// The `require_serving!` macro is defined in state.rs and used by handlers.rs,
// so state must be declared first.
#[macro_use]
pub(crate) mod state;
pub(crate) mod handlers;
pub(crate) mod rebuild;
