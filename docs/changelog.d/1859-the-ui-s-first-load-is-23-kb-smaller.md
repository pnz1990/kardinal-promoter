---
section: Changed
---

- **The UI's first load is 23 KB smaller** (#1859) — initial JS (gzip) went from 152.0 KB to 128.9 KB: the station map lays out with its own layered layout instead of dagre, and the fleet board, the map, the sidebar's virtual list, the ship dialog and the token form load with the route that shows them, each in its place's skeleton, so nothing moves. An `/env/` link no longer shifts the page when the drawer arrives. The station map's fleet label says when the fleet is paused and shows `maxUnavailable` as written (`stop at 10% (5) failed`), and a fleet target's drawer offers the fleet's Pause or Resume fleet and Roll back fleet
