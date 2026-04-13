---
description: "Signal the running agent to stop gracefully after it finishes its current item. Safe to run while the agent is active — it will not interrupt in-flight work."
---

You are signaling the otherness agent to stop after its current work completes.

## What this does

Creates `.otherness/stop-after-current` — a sentinel file the agent checks at the top of every loop cycle. The agent finishes whatever it is currently doing (implement → QA → merge), then exits cleanly before picking up the next item.

It does **not** interrupt in-flight work. It does **not** reset state. The next `/otherness.run` will resume exactly where it left off.

## Step 1 — Write the sentinel

```bash
python3 - << 'EOF'
import json, datetime, os

reason = "Manual stop requested via /otherness.pause"

# Write sentinel file
sentinel = {
    "requested_at": datetime.datetime.utcnow().strftime('%Y-%m-%dT%H:%M:%SZ'),
    "reason": reason
}
os.makedirs('.maqa', exist_ok=True)
with open('.otherness/stop-after-current', 'w') as f:
    json.dump(sentinel, f, indent=2)

# Record in state.json
try:
    with open('.otherness/state.json', 'r') as f:
        s = json.load(f)
    s['handoff'] = {
        "requested_at": sentinel["requested_at"],
        "reason": reason,
        "resume_with": "/otherness.run"
    }
    with open('.otherness/state.json', 'w') as f:
        json.dump(s, f, indent=2)
    print(f"Sentinel written. Agent will stop after current item completes.")
    in_flight = [(id, d['state']) for id, d in s.get('features', {}).items()
                 if d.get('state') in ('assigned', 'in_progress', 'in_review')]
    if in_flight:
        print(f"In-flight items (will complete before stopping):")
        for id, state in in_flight:
            print(f"  {id}: {state}")
    else:
        print("No in-flight items — agent will stop on next loop boundary.")
except Exception as e:
    print(f"state.json not updated ({e}), but sentinel file written.")
EOF
```

## Step 2 — Confirm

```bash
echo ""
echo "Sentinel: $(cat .otherness/stop-after-current)"
echo ""
echo "To cancel the stop signal: rm .otherness/stop-after-current"
echo "To resume:                 /otherness.run"
```
