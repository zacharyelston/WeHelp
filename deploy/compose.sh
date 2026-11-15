# Sourced by appliance.sh and backup.sh. Expects DEPLOY_DIR, ENV_FILE and
# DB_ENV to be set.
#
# Database passwords (#161) live only in deploy/db.env, the one copy that
# backups keep. The tracked compose files never name or interpolate them.
# Instead, each compose run writes them into two env files (gitignored, mode
# 600) that the services load with env_file:
#   db-owner.env   POSTGRES_PASSWORD and FLYWAY_PASSWORD, for db and migrate;
#   db-server.env  WEHELP_DATABASE_URL as wehelp_app, for the server only,
# so the server never holds the owner password.

render_db_env_files() {
  local owner_file="$DEPLOY_DIR/db-owner.env" server_file="$DEPLOY_DIR/db-server.env"
  if [[ ! -f "$DB_ENV" ]]; then
    rm -f "$owner_file" "$server_file"
    return 0
  fi
  local owner app
  owner="$(sed -n 's/^WEHELP_DB_PASSWORD=//p' "$DB_ENV")"
  app="$(sed -n 's/^WEHELP_DB_APP_PASSWORD=//p' "$DB_ENV")"
  (
    umask 077
    printf 'POSTGRES_PASSWORD=%s\nFLYWAY_PASSWORD=%s\n' "$owner" "$owner" > "$owner_file"
    printf 'WEHELP_DATABASE_URL=postgres://%s:%s@db:5432/wehelp?sslmode=disable\n' wehelp_app "$app" > "$server_file"
  )
}

# compose runs Docker Compose on the base and appliance files, reading .env
# (settings, WEHELP_DEMO) for interpolation.
compose() {
  local envs=()
  [[ -f "$ENV_FILE" ]] && envs+=(--env-file "$ENV_FILE")
  render_db_env_files
  docker compose ${envs[@]+"${envs[@]}"} -f "$DEPLOY_DIR/docker-compose.yml" -f "$DEPLOY_DIR/docker-compose.appliance.yml" "$@"
}
