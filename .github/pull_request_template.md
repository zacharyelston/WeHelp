## Summary

<!-- What changed and why. Link the issue: Closes #N -->

## Standards checklist

- [ ] No secrets committed; config via Viper only
- [ ] Ledger/audit writes use `ledger.Post` / `audit.Record` inside a tx (no UPDATE/DELETE on append-only tables)
- [ ] PHI-touching writes record an audit event in the same transaction
- [ ] New tables include `tenant_id`
- [ ] Migrations are numbered, forward-only, with a `Down`
- [ ] iOS changes edit `ios/project.yml`, not the generated `.xcodeproj`

## Test plan

<!-- Commands run and their results. Copy the issue's Verification section. -->
