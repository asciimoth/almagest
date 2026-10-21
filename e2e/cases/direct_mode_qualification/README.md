# Direct-mode qualification

This case checks the direct daemon process boundary across startup, a graph and
config replacement, snapshot creation and restore, and shutdown. The daemon must
have no child process and no socket other than its public admin socket.
