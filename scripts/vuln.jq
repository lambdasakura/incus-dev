# Reads a govulncheck JSON report (slurped) and prints one line per
# vulnerability this code calls: the advisory id, and what can be done.
#
# "update to X" is the only actionable one and the only one make vuln fails
# on. A fix that exists only under a different module path -- incus/v7 for a
# build on v6 -- is named as such rather than reported as no fix at all, which
# is what the first version of this did.

# Join each called finding to its advisory, so a fix that exists only under a
# different module path is reported as what it is rather than as "no fix".
# govulncheck always emits a config object. Without one the report is not a
# report, and an empty result would otherwise read as "no vulnerabilities".
if any(.[]; .config) | not then
  ("the report has no config object; govulncheck produced nothing usable" | halt_error(1))
else . end
|

def fixes: [.affected[]? | . as $a
  | ($a.ranges[]?.events[]?.fixed // empty) as $f
  | "\($a.package.name)@\($f)"] | unique;

(reduce (.[] | select(.osv)) as $o ({}; .[$o.osv.id] = ($o.osv | fixes))) as $fixed
| [ .[] | select(.finding) | .finding | select(.trace[0].function != null) ]
| map({osv, fixed_version})
| unique
| .[]
| [ .osv,
    (if .fixed_version then "update to \(.fixed_version)"
     elif ($fixed[.osv] // []) | length > 0 then "only in \(($fixed[.osv] | join(", ")))"
     else "no fix released" end) ]
| @tsv
