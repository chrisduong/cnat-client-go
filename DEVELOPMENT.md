# Development guide

## Setup your own K8s controller repo

Clone the repo `https://github.com/kubernetes/sample-controller` with your own Controller App name.

Create your own Git repo, make it your own Origin.

Open the `go.mod` file, change the module name to your module name (*github.com/chrisduong/cnat-client-go*)

Download your dependencies

```sh
go mod vendor
```

### Rename

Rename the folder the *pkg/apis/cnat* to *pkg/apis/yourown*

Rename *module* path to *github.com/chrisduong/cnat-client-go*

## Verify

Run `hack/verify-codegen.sh` to verify code changes.
