# Changelog

## Unreleased

- Preserve DKG coefficient files through speculative ceremony ack proposals so
  validators can retry `MsgAckExecutiveAuthorityKey` if CometBFT advances to a
  later round before their proposal commits. Coefficients are now cleaned up only
  after committed state moves the round past the ceremony ack window.
