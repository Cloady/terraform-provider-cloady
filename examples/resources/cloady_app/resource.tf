# Deploy a Git repository. Cloady detects the stack and builds it.
resource "cloady_app" "api" {
  workspace = cloady_workspace.acme.slug
  name      = "API"
  region    = "eu1"

  git = {
    repository_url = "https://github.com/acme/api"
    branch         = "main"
  }

  # Available before the first build; updates trigger one deployment after all
  # managed values are saved. Do not also manage these keys with cloady_variable.
  values = { LOG_LEVEL = "info" }
}

# Or install a catalog application.
resource "cloady_app" "db" {
  workspace = cloady_workspace.acme.slug
  name      = "Postgres"
  region    = "eu1"
  catalog   = "postgres"

  # Existing volumes cannot shrink. Replacement apps create fresh storage.
  volume_sizes = { data = 20 }
}
