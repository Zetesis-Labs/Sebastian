env "local" {
  src = "file://db/schema.sql"
  dev = getenv("ATLAS_DEV_DATABASE_URL")
  migration {
    dir = "file://internal/database/migrations"
  }
  format {
    migrate {
      diff = "{{ sql . \"  \" }}"
    }
  }
}
