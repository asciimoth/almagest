Runs a command from the daemon's protected command list when the
[Trigger](@port/left/Trigger) input receives an event. The workspace cannot
supply an executable or a full command line. It can select only commands that
have `AllowNodes` enabled in protected config.

The daemon reads the protected command list at startup. Restart the daemon after
you change this list. For example, this entry makes one command available to
Command nodes and defines one required value:

```hujson
CommandRunner: {
  Commands: {
    notify: {
      Command: ["/usr/local/bin/notify", "--message", "${message}"]
      AllowNodes: true
      Slots: {
        message: {
          MaxLength: 256
          Required: true
        }
      }
    }
  }
}
```

Select an available command in the node menu. The node adds one String input for
each slot declared by that command. It stores the latest value received by each
input. When a trigger arrives, the node takes a snapshot of these values and
queues one command request. An unconnected optional slot is omitted. Each slot
input accepts one connection.

The command runner checks required values, byte limits, regular-expression
patterns, and empty-value rules before it starts the process. A pattern must
match the complete value. Each slot has a configured byte limit, with a hard
maximum of 32,768 bytes. A rejected value does not start the command. Changing
the selected command removes inputs that the new command does not use and
discards their stored values.

Protected config also controls the command arguments, environment, process
credentials, execution mode, and timeout. Use a direct executable and separate
arguments where possible. If the protected command starts a shell, the shell
interprets substituted slot values. Use restrictive slot patterns for these
values.

A synchronous command runs in order with other synchronous commands. Its default
timeout is 10 seconds when `Timeout` is zero. A background command can run at
the same time as other commands, and `Timeout` does not apply to it. The Command
node does not wait for either mode to finish.

The node has no result output. Standard output and standard error from the
process are discarded. The daemon log reports the command name, completion
status, duration, and validation or launch errors.

If a saved command is no longer available after a restart, the node reports
`unavailable command`, removes its slot inputs, and does not run it when
triggered.

On Windows, command entries cannot set `UID` or `GID`. In production Linux
isolation, all command entries must use one shared absolute executable, and
per-command `UID` and `GID` values are not supported. The `linux-user-v1`
isolation profile does not support configured commands. Daemon startup stops if
this profile requires a command.
