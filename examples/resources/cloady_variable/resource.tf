# Stored setting; takes effect at the next deployment. Use cloady_app.values
# instead when Terraform should deploy the change as part of the same apply.
resource "cloady_variable" "database_url" {
  workspace = cloady_workspace.acme.slug
  app       = cloady_app.api.slug
  region    = cloady_app.api.region
  key       = "DATABASE_URL"
  value     = var.database_url
  is_secret = true
}
