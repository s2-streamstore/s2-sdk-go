# Dev guide

Install `task`: https://taskfile.dev/

```bash
# Generate code
task gen

# Run an example (Should take care of generating code if required)
task example NAME=basic
```

A bunch of code lives inside `syncgen` directory. The generated code will be `s2/*.sync.go`.
This is done so we can replicate the comments from proto without copypasta.

Ensure there is no business logic inside the `syncgen` directory.
Make sure it's just some structures and function declarations.

* `syncgen/types.sync.tmpl`: Has all the parallel types from `pb/s2.pb.go`
* `syncgen/client.sync.tmpl`: Has all the parallel client method declarations from `pb/s2_grpc.pb.go`