# herdr-dispatch

The dispatcher for the [herdr-tasks](https://github.com/husniadil/herdr-tasks)
board: `hdis` watches for ready tasks, brings up a worker agent in a
[Herdr](https://herdr.dev) pane for each one, delivers the task's goal, tracks
the worker, and hands off at review — where the board's own review gate takes
over. The board stays the ledger; this binary is only execution policy.

Under construction. The decision core lands first; the adapters that drive
`htask` and `herdr` follow it.

## License

MIT.
