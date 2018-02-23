# Contributing
To develop on this project, please fork the repo and clone into your `$GOPATH`.

Dependencies are **not** checked in so please download those seperately.
Download the dependencies using [`glide`](https://github.com/Masterminds/glide).

```bash
cd $GOPATH/src/github.com # Create this directory if it doesn't exist
git clone git@github.com:<YOUR_FORK>/spot-rescheduler pusher/spot-rescheduler
glide install -v # Installs dependencies to vendor folder.
```

The main package is within `rescheduler.go` and an overview of it's operating logic is described in the [Readme](README.md/#operating-logic).

If you want to run the rescheduler locally you must have a valid `kubeconfig` file somewhere on your machine and then run the program with the flag `--running-in-cluster=false`.

## Pull Requests and Issues
We track bugs and issues using Github .

If you find a bug, please open an Issue.

If you want to fix a bug, please fork, fix the bug and open a PR back to this repo.
Please mention the open bug issue number within your PR if applicable.

### Tests
Unit tests are covering the decision making parts of this code and can be run using the built in Go test suite.

To run the tests: `go test $(glide novendor)`
