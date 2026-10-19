---
section: Added
---

- **Frozen environments on the map and the fleet board** (#1974). A PromotionStep a freeze holds records it in `status.frozenBy` (its name and since when) and clears it when no freeze holds it any more, or a pause holds it instead. The Pipeline's `status.environments[].frozenBy` names the freeze per environment. The station map and the fleet board mark such a station frozen ("Frozen by <freeze>", in its own colour, counted under **Needs attention**), the stage drawer reads the field rather than the step's message, and `kardinal get pipelines` and `kardinal status` show "Frozen by <freeze>" ([Freezes in the UI](ui.md#freezes)).
