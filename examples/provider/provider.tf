terraform {
  required_providers {
    cloady = {
      source  = "cloady/cloady"
      version = "~> 0.1"
    }
  }
}

# The token defaults to CLOADY_TOKEN. Mint one in the dashboard under
# Account -> Tokens. Workspace administration needs full scope; app, variable,
# and domain writes need deploy or full scope. Read scope supports other lookups.
provider "cloady" {}
