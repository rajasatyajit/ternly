"""One line of progress from a suite run's state.json (bench/suite/plan.sh status)."""
import json
import sys

s = json.load(open(sys.argv[1]))
done = s["done_this_run"] + s["skipped_already_done"]
fin = " (finished)" if s.get("finished") else ""
print(f"  {s['out']}: arm={s['arm'] or '-'} {done}/{s['total']} done; running {s['running']}; last: {s['last']}; updated {s['updated'][:19]}Z{fin}")
