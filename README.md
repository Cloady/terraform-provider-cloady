# Terraform Provider for Cloady

Manage [Cloady](https://cloady.com) workspaces, applications, environment variables and
custom domains as infrastructure as code.

```hcl
terraform {
  required_providers {
    cloady = {
      source  = "cloady/cloady"
      version = "~> 0.1"
    }
  }
}

provider "cloady" {} # reads CLOADY_TOKEN

resource "cloady_app" "api" {
  workspace = "acme"
  name      = "API"
  region    = "eu1"
  git       = { repository_url = "https://github.com/acme/api" }
  values    = { LOG_LEVEL = "info" }
}

output "url" {
  value = cloady_app.api.endpoints[0]
}
```

## Resources

| Resource | Manages |
|---|---|
| `cloady_app` | An application deployed from a Git repository or the catalog |
| `cloady_workspace` | A workspace |
| `cloady_variable` | One application environment variable |
| `cloady_domain` | A custom domain on an application |

Data sources: `cloady_workspace`, `cloady_regions`.

Full reference: [registry.terraform.io/providers/cloady/cloady](https://registry.terraform.io/providers/cloady/cloady/latest/docs).

## Authentication

Mint a personal API token in the dashboard under **Account → Tokens** and export it:

```sh
export CLOADY_TOKEN=cldy_...
```

Workspace administration requires a full-scope token. App, variable, and domain
writes require deploy or full scope; a read token supports lookups other than
variables. Set `base_url` (or `CLOADY_CONTROL_PLANE_URL`) to target another control plane.

Use the app's `values` map for deployment variables: they are supplied before the
first build, and changes are saved together before one deployment. Removing a key
removes that managed variable; unrelated variables remain untouched. Do not manage
the same key with both `values` and `cloady_variable`. Standalone variable resources
change stored settings and take effect at the next deployment.

## Things worth knowing before you apply

- **Destroying an application deletes its data.** Cloady removes the application's
  namespace, and its volumes go with it. Put `prevent_destroy` on anything stateful.
- **Region, environment, workspace and Git repository are immutable.** Changing one
  replaces the application, which is a delete followed by a create.
- **Existing volumes only grow.** In-place changes are checked against
  `effective_volume_sizes`, including template defaults. A replacement app may use
  smaller volumes because it creates new storage.
- **Variable values live in Terraform state in plaintext**, including secrets. The
  `is_secret` flag encrypts the value inside Cloady, not inside your state file. Use a
  remote backend with encryption at rest.
- **A successful apply is not a healthy application.** Create returns as soon as the
  deployment starts; `status` reports what Cloady observed at that moment.

## Development

Requires the Go version in `go.mod` and Terraform for the acceptance tests.

```sh
make check     # gofmt, vet, unit tests, build
make testacc   # acceptance tests against an in-process API fixture
make docs      # regenerate docs/ from the schema and examples
```

Acceptance tests need no Cloady account: they run Terraform against a local HTTP
fixture and create nothing billable.

Releasing is documented in [PUBLISHING.md](PUBLISHING.md).

## License

[MPL-2.0](LICENSE).
