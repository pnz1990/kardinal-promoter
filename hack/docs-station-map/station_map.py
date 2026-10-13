# Copyright 2026 The kardinal-promoter Authors.
# Licensed under the Apache License, Version 2.0
"""The docs station map: the product UI's pipeline map, drawn at build time.

A MkDocs hook (mkdocs.yml `hooks:`). A docs page declares a diagram in a fenced block:

    ```station-map
    title: A version moving to prod
    lead: {into: test, label: Your CI, note: creates a version}
    stations:
      test: {state: verified, notes: [commit · health check]}
      uat: {state: verified}
      prod: {state: waiting, notes: [pull request · merge], status: Waiting for PR review}
    edges:
      - test -> uat
      - {from: uat, to: prod, gates: [no-weekend-deploys]}
    thread: [test, uat, prod]
    ```

and gets a static SVG (role=img, with a title and a description) that uses the markup
of web/src/stationMap/StationMap.tsx at its Map zoom: the same class names, so the
same stylesheet, web/src/styles/StationMap.css, draws it. The hook ships that file
into the site as stylesheets/station-map.css; docs/stylesheets/k-map.css only adds
what a static page needs (the figure, the source label of a lead, the home page's
animation). The geometry mirrors web/src/stationMap/geometry.ts (octilinear edges,
gates on the run into the environment they guard) and reads its lengths from the
design tokens, web/tokens/tokens.json. No JavaScript runs in the browser.

A ```component-map block draws a component architecture (zones, components, labelled
edges) with the same parts: component_map.py.

The overrides/ templates call the same renderer: {{ station_map(spec) }}, where
spec is the YAML above as a string.

Spec fields:
  title     required. The SVG's <title>.
  desc      the SVG's <desc>; written from the stations, edges and gates when absent.
  caption   a <figcaption> under the map.
  lead      {into, label, note}: a dashed lead into a first station, with where the
            version comes from (the UI draws a lead into a first station with gates).
  stations  id: state, or id: {name, state, notes: [...], status, lane}. state is a
            tone of the UI: verified, promoting, waiting, blocked, failed, held,
            superseded, pending. notes are mono lines under the dot; status is the
            tone-coloured state line under them. lane places a station above (<0)
            or below (>0) the main line; by default a fan-out spreads its targets
            around its source and a fan-in sits between its sources.
  edges     "a -> b", "a -> b, c" (a fan-out), or {from, to, gates}. A gate is a
            name, or {name, state: passed|waiting|blocked, approval: "1/2"}.
  thread    the stations the followed version has reached or is moving into: the
            red thread runs on every edge between two of them (and on the lead).
  play      true on the home page: the thread is drawn edge by edge, waits at a gate
            that holds, and the gate opens (docs/stylesheets/k-map.css).
  bare      true: no frame (the home page's hero).
"""

from __future__ import annotations

import hashlib
import html
import json
import math
import os
import re
from typing import Any, Dict, List, Optional, Sequence, Tuple

import yaml

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", ".."))
TOKENS_JSON = os.path.join(ROOT, "web", "tokens", "tokens.json")
UI_CSS = os.path.join(ROOT, "web", "src", "styles", "StationMap.css")
SITE_CSS = "stylesheets/station-map.css"

Point = Tuple[float, float]


def _tokens() -> Dict[str, Any]:
    with open(TOKENS_JSON, encoding="utf-8") as f:
        return json.load(f)


def _px(v: str) -> float:
    """A token dimension in px (rem is 16px, as web/src/stationMap/geometry.ts has it)."""
    if v.endswith("rem"):
        return float(v[:-3]) * 16
    if v.endswith("px"):
        return float(v[:-2])
    return float(v)


_T = _tokens()
_G = _T["graph"]
R = _px(_G["station"]["r"]["$value"])
CHAMFER = _px(_G["chamfer"]["$value"])
GATE_TICK = _px(_G["gateTick"]["$value"])
RANK_SEP = _px(_G["rankSep"]["$value"])
NODE_SEP = _px(_G["nodeSep"]["$value"])
LABEL_GAP = _px(_G["labelGap"]["$value"])
GRID_GAP = _px(_G["grid"]["gap"]["$value"])
TEXT = {k: _px(v["$value"]) for k, v in _T["text"].items() if k in ("2xs", "xs", "sm", "md", "lg")}

# The Map zoom of StationMap.tsx (data-level="map"): the name at text-sm in the label
# face, its lines at text-xs, a line every 14 map units (lineOf), the name 6 above the
# dot. Gate labels are text-2xs in the condensed face, GATE_CH a character, GATE_LEADER
# above the bar (StationMap.tsx). Map, not Detail: a docs column is narrower than the
# UI's canvas, and Map is the zoom the UI opens a mid-sized pipeline at.
LEVEL = "map"
NAME_PX = TEXT["sm"]
SUB_PX = TEXT["xs"]
GATE_PX = TEXT["2xs"]
LINE = 14.0
NAME_RISE = 6.0
GATE_CH = 6.0
GATE_LEADER = 8.0
GATE_STEP = 6.0  # geometry.ts: the spacing of gate marks on one edge
# A character's width as a share of the font size, measured in Chromium on the built
# site (getComputedTextLength): the mono face 0.632, the label face at most 0.49 (the
# widest names). Rounded up, so an estimate never runs into the next label.
MONO_EM = 0.64
DISPLAY_EM = 0.52
ASCENT = 0.8  # the share of a font size above the baseline
THREAD_GAP = GATE_TICK  # geometry.ts THREAD_GAP_PX
LEAD = GATE_TICK * 4 + GATE_TICK / 2 + 1  # geometry.ts LEAD
MARGIN = NODE_SEP - GRID_GAP * 2  # geometry.ts MARGIN

TONES = ("verified", "promoting", "waiting", "blocked", "failed", "held", "superseded", "pending")
TONE_WORD = {
    "verified": "verified", "promoting": "promoting", "waiting": "waiting for a person",
    "blocked": "blocked", "failed": "failed", "held": "held", "superseded": "replaced",
    "pending": "not reached",
}
GATE_STATES = ("passed", "waiting", "blocked")

# The UI classes the map is drawn with. on_config fails the build when the UI stylesheet
# no longer styles one of them, so the docs cannot silently drift from the product.
UI_CLASSES = (
    "station-map", "station-map__svg", "station-map__edges", "station-map__edge", "station-map__lead",
    "station-map__thread", "station-map__thread-path--flow", "station-map__thread-path--wait",
    "station-map__fray", "station-map__nodes", "station", "station__dot", "station__ring2", "station__notch",
    "station__tick", "station__arc", "station__name", "station__sub", "station__incoming",
    "station-map__gates", "station-map__gate", "station-map__gate-bar", "station-map__gate-leader",
    "station-map__gate-label", "station-map__gate-quorum", "group__frame",
)


class SpecError(ValueError):
    pass


# ── geometry (web/src/stationMap/geometry.ts) ─────────────────────────────────


def gate_run(n: int) -> float:
    return GATE_TICK * 4 + (n - 1) * GATE_STEP if n > 0 else 0.0


def gate_offset(i: int) -> float:
    return GATE_TICK * 3 + i * GATE_STEP


def octilinear(a: Point, b: Point, tail: float = 0.0) -> List[Point]:
    """a → b with horizontal runs and 45° bends; the diagonal ends `tail` before b."""
    x1, y1 = a
    x2, y2 = b
    dy = y2 - y1
    if abs(dy) < 0.5:
        return [a, b]
    sign = 1 if dy > 0 else -1
    dx = x2 - x1
    ady = abs(dy)
    lead = min(CHAMFER, max(0.0, dx / 4))
    end = max(lead, min(tail, dx - lead - 2 * min(CHAMFER, ady / 2)))
    if dx - lead - end >= ady:
        xd = x2 - end - ady
        return [a, (xd, y1), (xd + ady, y2), b]
    leg = max(0.0, min(CHAMFER, (dx - lead - end) / 2, ady / 2))
    xm = x2 - end - 2 * leg
    return [a, (xm, y1), (xm + leg, y1 + sign * leg), (xm + leg, y2 - sign * leg), (xm + 2 * leg, y2), b]


def _seg_len(p: Point, q: Point) -> float:
    return math.hypot(q[0] - p[0], q[1] - p[1])


def length_of(points: Sequence[Point]) -> float:
    return sum(_seg_len(points[i - 1], points[i]) for i in range(1, len(points)))


def point_before_end(points: Sequence[Point], d: float) -> Tuple[float, float, float, float]:
    """The point d before the end of a polyline, and the unit direction there."""
    left = d
    for i in range(len(points) - 1, 0, -1):
        (x1, y1), (x2, y2) = points[i - 1], points[i]
        seg = math.hypot(x2 - x1, y2 - y1)
        if seg == 0:
            continue
        ux, uy = (x2 - x1) / seg, (y2 - y1) / seg
        if left <= seg:
            return x2 - ux * left, y2 - uy * left, ux, uy
        left -= seg
    x, y = points[0]
    return x, y, 1.0, 0.0


def split_before_end(points: Sequence[Point], d: float) -> Tuple[List[Point], List[Point]]:
    """The polyline cut d before its end: the part before the cut and the part after."""
    total = length_of(points)
    keep = max(0.0, total - d)
    head: List[Point] = [points[0]]
    walked = 0.0
    for i in range(1, len(points)):
        seg = _seg_len(points[i - 1], points[i])
        if walked + seg >= keep:
            t = (keep - walked) / seg if seg else 0
            cut = (points[i - 1][0] + (points[i][0] - points[i - 1][0]) * t,
                   points[i - 1][1] + (points[i][1] - points[i - 1][1]) * t)
            head.append(cut)
            return head, [cut] + list(points[i:])
        head.append(points[i])
        walked += seg
    return head, [points[-1]]


def path_of(points: Sequence[Point]) -> str:
    return " ".join(f"{'M' if i == 0 else 'L'}{_n(x)} {_n(y)}" for i, (x, y) in enumerate(points))


def _n(v: float) -> str:
    r = round(v, 1)
    return str(int(r)) if r == int(r) else str(r)


def text_w(s: str, px: float, em: float = MONO_EM) -> float:
    return len(s) * px * em


# ── the spec ─────────────────────────────────────────────────────────────────


class Gate:
    def __init__(self, raw: Any):
        if isinstance(raw, str):
            raw = {"name": raw}
        if not isinstance(raw, dict) or not raw.get("name"):
            raise SpecError(f"a gate is a name or {{name, state}}: {raw!r}")
        self.name = str(raw["name"])
        self.state = str(raw.get("state", "passed"))
        if self.state not in GATE_STATES:
            raise SpecError(f"gate {self.name}: state is one of {', '.join(GATE_STATES)}, not {self.state!r}")
        self.approval = raw.get("approval")
        # Play mode: the version waits here, then the gate opens.
        self.opens = bool(raw.get("opens", False))

    @property
    def holding(self) -> bool:
        return self.state in ("waiting", "blocked")


class Station:
    def __init__(self, sid: str, raw: Any):
        if raw is None:
            raw = {}
        if isinstance(raw, str):
            raw = {"state": raw}
        if not isinstance(raw, dict):
            raise SpecError(f"station {sid}: a state or a mapping, not {raw!r}")
        unknown = set(raw) - {"name", "state", "notes", "status", "lane"}
        if unknown:
            raise SpecError(f"station {sid}: unknown fields {sorted(unknown)}")
        self.id = sid
        self.name = str(raw.get("name", sid))
        self.state = str(raw.get("state", "verified"))
        if self.state not in TONES:
            raise SpecError(f"station {sid}: state is one of {', '.join(TONES)}, not {self.state!r}")
        notes = raw.get("notes", [])
        self.notes = [str(n) for n in ([notes] if isinstance(notes, str) else notes)]
        self.status = str(raw["status"]) if raw.get("status") else None
        self.lane: Optional[float] = float(raw["lane"]) if raw.get("lane") is not None else None
        self.rank = 0
        self.x = 0.0
        self.y = 0.0

    @property
    def lines(self) -> List[Tuple[str, str]]:
        out = [("station__sub", n) for n in self.notes]
        if self.status:
            out.append(("station__incoming", self.status))
        return out

    @property
    def name_width(self) -> float:
        return text_w(self.name, NAME_PX, DISPLAY_EM)

    @property
    def lines_width(self) -> float:
        return max([0.0] + [text_w(t, SUB_PX) for _, t in self.lines])

    @property
    def width(self) -> float:
        """From the dot's left edge, where the labels start, to the end of the longest."""
        return max(self.name_width, self.lines_width)

    @property
    def above(self) -> float:
        return R + NAME_RISE + NAME_PX * ASCENT

    @property
    def below(self) -> float:
        return R + LINE * len(self.lines) + SUB_PX * (1 - ASCENT)


class Edge:
    def __init__(self, a: str, b: str, gates: Sequence[Gate]):
        self.a, self.b, self.gates = a, b, list(gates)
        self.points: List[Point] = []
        # The first gate's label. A fan-out whose edges carry the same gates labels
        # them once, on its first edge: one label per gate, not one per environment.
        self.labelled = bool(self.gates)

    @property
    def tail(self) -> float:
        """The run into the target: its gates, and room for their label above it, so the
        label never reaches back over a bend."""
        if not self.gates:
            return 0.0
        # The same on every edge of a fan-out (labelled or not), so its bends line up.
        return max(gate_run(len(self.gates)), gate_offset(0) + len(_gate_label(self.gates)) * GATE_CH + LABEL_GAP)


class Spec:
    def __init__(self, raw: Any):
        if not isinstance(raw, dict):
            raise SpecError("a station map is a YAML mapping")
        unknown = set(raw) - {"title", "desc", "caption", "lead", "stations", "edges", "thread", "play", "bare"}
        if unknown:
            raise SpecError(f"unknown fields {sorted(unknown)}")
        if not raw.get("title"):
            raise SpecError("title is required (the SVG's accessible name)")
        self.title = str(raw["title"])
        self.desc = str(raw["desc"]) if raw.get("desc") else None
        self.caption = str(raw["caption"]) if raw.get("caption") else None
        self.play = bool(raw.get("play", False))
        self.bare = bool(raw.get("bare", False))
        stations = raw.get("stations") or {}
        if not isinstance(stations, dict) or not stations:
            raise SpecError("stations: a mapping of id to state or fields")
        self.stations: Dict[str, Station] = {str(k): Station(str(k), v) for k, v in stations.items()}
        self.edges: List[Edge] = []
        for e in raw.get("edges") or []:
            self._edge(e)
        lead = raw.get("lead")
        self.lead: Optional[Dict[str, str]] = None
        if lead:
            if not isinstance(lead, dict) or lead.get("into") not in self.stations:
                raise SpecError(f"lead: {{into: <station>, label, note}}, not {lead!r}")
            self.lead = {k: str(lead.get(k, "")) for k in ("into", "label", "note")}
        thread = raw.get("thread") or []
        for t in thread:
            if str(t) not in self.stations:
                raise SpecError(f"thread: unknown station {t!r}")
        self.thread = [str(t) for t in thread]

    def _edge(self, e: Any) -> None:
        gates: List[Gate] = []
        if isinstance(e, str):
            m = re.fullmatch(r"\s*([^,>]+?)\s*->\s*(.+?)\s*", e)
            if not m:
                raise SpecError(f"an edge is 'a -> b' or 'a -> b, c': {e!r}")
            a, bs = m.group(1), [b.strip() for b in m.group(2).split(",")]
        elif isinstance(e, dict):
            a, to = str(e.get("from", "")), e.get("to", [])
            bs = [str(b) for b in ([to] if isinstance(to, str) else to)]
            gates = [Gate(g) for g in e.get("gates", [])]
        else:
            raise SpecError(f"an edge is 'a -> b' or {{from, to, gates}}: {e!r}")
        for s in [a] + bs:
            if s not in self.stations:
                raise SpecError(f"edge {e!r}: unknown station {s!r}")
        for i, b in enumerate(bs):
            edge = Edge(a, b, gates)
            edge.labelled = bool(gates) and i == 0
            self.edges.append(edge)

    def into(self, sid: str) -> List[Edge]:
        return [e for e in self.edges if e.b == sid]

    def out(self, sid: str) -> List[Edge]:
        return [e for e in self.edges if e.a == sid]


# ── layout ───────────────────────────────────────────────────────────────────


def layout(spec: Spec) -> None:
    st = spec.stations
    order = list(st)
    # Ranks: the longest path from a source.
    indeg = {s: len(spec.into(s)) for s in order}
    queue = [s for s in order if indeg[s] == 0]
    seen: List[str] = []
    while queue:
        s = queue.pop(0)
        seen.append(s)
        for e in spec.out(s):
            st[e.b].rank = max(st[e.b].rank, st[s].rank + 1)
            indeg[e.b] -= 1
            if indeg[e.b] == 0:
                queue.append(e.b)
    if len(seen) != len(order):
        raise SpecError("the edges have a cycle; a station map is a DAG")
    ranks = max(s.rank for s in st.values()) + 1
    by_rank = [[s for s in seen if st[s].rank == r] for r in range(ranks)]

    # Lanes: a fan-out spreads its targets around its source, a fan-in sits between its
    # sources, a chain keeps its lane. An explicit lane wins.
    lane: Dict[str, float] = {}
    for r, ids in enumerate(by_rank):
        for i, s in enumerate(ids):
            if st[s].lane is not None:
                lane[s] = st[s].lane  # type: ignore[assignment]
                continue
            ins = spec.into(s)
            if not ins:
                srcs = [x for x in ids if not spec.into(x)]
                lane[s] = srcs.index(s) - (len(srcs) - 1) / 2
            elif len(ins) == 1:
                a = ins[0].a
                sibs = [e.b for e in spec.out(a)]
                lane[s] = lane[a] + (sibs.index(s) - (len(sibs) - 1) / 2 if len(sibs) > 1 else 0)
            else:
                lane[s] = sum(lane[e.a] for e in ins) / len(ins)
        lanes = sorted(lane[s] for s in ids)
        for p, q in zip(lanes, lanes[1:]):
            if q - p < 1 - 1e-9:
                raise SpecError(f"stations {', '.join(ids)} share a column too closely: set lane: on one of them")

    above = max(s.above for s in st.values())
    below = max(s.below for s in st.values())
    lane_h = above + below + NODE_SEP
    for s in st.values():
        s.y = lane[s.id] * lane_h

    # Columns (x is where a station's labels start, the dot's left edge): a station's
    # labels end before the next ones on its lane start; an edge's run out of its source
    # clears the source's labels on the side it bends to; a gate's label, above the run
    # into the target, starts after the source's name.
    x = [0.0] * ranks
    for r in range(1, ranks):
        need = x[r - 1] + RANK_SEP
        for b in (st[i] for i in by_rank[r]):
            for a in st.values():
                if a.rank < r and abs(lane[a.id] - lane[b.id]) < 1 - 1e-9:
                    need = max(need, x[a.rank] + a.width + LABEL_GAP * 2)
        for e in spec.edges:
            a, b = st[e.a], st[e.b]
            if b.rank != r:
                continue
            ady = abs(b.y - a.y)
            if ady:
                side = a.lines_width if b.y > a.y else a.name_width
                end = max(e.tail, CHAMFER)
                need = max(need, x[a.rank] + max(side, 2 * R) + LABEL_GAP + ady + end)
            if e.labelled:
                label = len(_gate_label(e.gates)) * GATE_CH
                need = max(need, x[a.rank] + max(a.name_width, 2 * R) + LABEL_GAP + label + gate_offset(0))
        x[r] = need
    for s in st.values():
        s.x = x[s.rank] + R

    for e in spec.edges:
        a, b = st[e.a], st[e.b]
        e.points = octilinear((a.x + R, a.y), (b.x - R, b.y), e.tail)
        # A straight edge that runs through a station between its ends.
        for s in st.values():
            if s.id in (e.a, e.b) or abs(s.y - a.y) > 0.5 or abs(b.y - a.y) > 0.5:
                continue
            if a.x < s.x < b.x:
                raise SpecError(f"edge {e.a} -> {e.b} runs through {s.id}: set lane: on {s.id}")


def _gate_label(gates: Sequence[Gate]) -> str:
    if not gates:
        return ""
    return gates[0].name + (f" +{len(gates) - 1}" if len(gates) > 1 else "")


# ── rendering ────────────────────────────────────────────────────────────────


def _e(s: str) -> str:
    return html.escape(s, quote=True)


def _describe(spec: Spec) -> str:
    parts = []
    if spec.lead:
        parts.append(f"{spec.lead['label']} {spec.lead['note']}".strip() + f", into {spec.stations[spec.lead['into']].name}.")
    for s in spec.stations.values():
        bits = [TONE_WORD[s.state]] + s.notes + ([s.status] if s.status else [])
        parts.append(f"{s.name}: {'; '.join(bits)}.")
    for e in spec.edges:
        g = ""
        if e.gates:
            g = " through gate " + ", ".join(f"{x.name} ({x.state})" for x in e.gates)
        parts.append(f"{spec.stations[e.a].name} to {spec.stations[e.b].name}{g}.")
    if spec.thread:
        parts.append("The followed version: " + ", ".join(spec.stations[t].name for t in spec.thread) + ".")
    return " ".join(parts)


def _thread_kind(spec: Spec, e: Edge) -> str:
    tone = spec.stations[e.b].state
    return {"promoting": "flow", "waiting": "wait", "failed": "snap", "held": "held"}.get(tone, "solid")


def render(raw: Any, uid: Optional[str] = None) -> str:
    spec = Spec(raw)
    layout(spec)
    st = spec.stations
    uid = uid or "k-map-" + hashlib.sha1(json.dumps(raw, sort_keys=True, default=str).encode()).hexdigest()[:8]
    thread = set(spec.thread)
    clock = _Clock(spec) if spec.play else None

    edges, threads, gates, nodes = [], [], [], []
    lead_pts: Optional[List[Point]] = None
    if spec.lead:
        s = st[spec.lead["into"]]
        gated = [e for e in spec.into(s.id) if e.gates]
        # Long enough that its source label and note end before the station's labels start.
        words = max(text_w(spec.lead["label"], SUB_PX, DISPLAY_EM), text_w(spec.lead["note"], SUB_PX))
        length = max(LEAD, words + NODE_SEP, gate_run(len(gated[0].gates)) + GATE_TICK / 2 if gated else 0)
        lead_pts = [(s.x - R - length, s.y), (s.x - R, s.y)]
        edges.append(f'<path d="{path_of(lead_pts)}" class="station-map__lead"/>')
        if s.id in thread:
            threads.append(_thread_path(lead_pts, "solid", clock.lead() if clock else None))

    for e in spec.edges:
        edges.append(f'<path d="{path_of(e.points)}" class="station-map__edge"/>')
        if e.a in thread and e.b in thread:
            kind = _thread_kind(spec, e)
            hold = next((i for i, g in enumerate(e.gates) if g.holding), -1)
            opens = next((i for i, g in enumerate(e.gates) if g.opens), -1)
            pts = e.points
            if hold >= 0:
                pts, _ = split_before_end(pts, min(length_of(pts), gate_offset(hold) + THREAD_GAP))
                threads.append(_thread_path(pts, "solid", clock.edge(e) if clock else None))
            elif kind == "snap":
                pts, _ = split_before_end(pts, R + THREAD_GAP / 2)
                tip = pts[-1]
                threads.append(_thread_path(pts, kind, clock.edge(e) if clock else None)
                               + f'<path class="station-map__fray" d="M{_n(tip[0])} {_n(tip[1])} l4 -4 M{_n(tip[0])} {_n(tip[1])} l4 4"/>')
            elif clock and opens >= 0:
                head, tail = split_before_end(pts, gate_offset(opens) + THREAD_GAP)
                t1, t2 = clock.edge_split(e)
                threads.append(_thread_path(head, kind, t1) + _thread_path(tail, kind, t2))
            else:
                threads.append(_thread_path(pts, kind, clock.edge(e) if clock else None))
        for i, g in enumerate(e.gates):
            gates.append(_gate(e, g, i, clock))

    for s in st.values():
        nodes.append(_station(s, clock.reach(s.id) if clock and s.id in thread else None))

    texts: List[str] = []
    if spec.lead and lead_pts:
        (lx, ly) = lead_pts[0]
        texts.append(f'<text x="{_n(lx)}" y="{_n(ly - R - NAME_RISE)}" class="k-map__source">{_e(spec.lead["label"])}</text>')
        if spec.lead["note"]:
            texts.append(f'<text x="{_n(lx)}" y="{_n(ly + R + LINE)}" class="k-map__source-note">{_e(spec.lead["note"])}</text>')

    # Bounds: every dot, label, edge and gate label.
    xs: List[float] = []
    ys: List[float] = []
    for s in st.values():
        xs += [s.x - R, s.x - R + s.width]
        ys += [s.y - s.above, s.y + s.below]
    for e in spec.edges:
        for (px, py) in e.points:
            xs.append(px)
            ys.append(py)
        if e.gates:
            gx, gy, _, _ = point_before_end(e.points, gate_offset(0))
            xs += [gx - len(_gate_label(e.gates)) * GATE_CH, gx + GATE_TICK]
            ys += [gy - GATE_TICK - GATE_LEADER - 4 - GATE_PX, gy + GATE_TICK * 2]
    if spec.lead and lead_pts:
        lx, ly = lead_pts[0]
        xs += [lx, lx + text_w(spec.lead["note"], SUB_PX), lx + text_w(spec.lead["label"], SUB_PX)]
        ys += [ly - R - NAME_RISE - NAME_PX, ly + R + LINE + SUB_PX]
    x0, y0 = min(xs) - MARGIN, min(ys) - MARGIN
    w, h = max(xs) - min(xs) + 2 * MARGIN, max(ys) - min(ys) + 2 * MARGIN

    desc = spec.desc or _describe(spec)
    tid, did = f"{uid}-title", f"{uid}-desc"
    svg = (
        f'<svg class="station-map__svg" viewBox="{_n(x0)} {_n(y0)} {_n(w)} {_n(h)}" width="{_n(w)}" height="{_n(h)}" '
        f'role="img" aria-labelledby="{tid} {did}" focusable="false">'
        f'<title id="{tid}">{_e(spec.title)}</title><desc id="{did}">{_e(desc)}</desc>'
        f'<g class="station-map__edges">{"".join(edges)}</g>'
        f'<g class="station-map__thread">{"".join(threads)}</g>'
        f'<g class="station-map__nodes">{"".join(nodes)}{"".join(texts)}</g>'
        f'<g class="station-map__gates">{"".join(gates)}</g>'
        "</svg>"
    )
    # The narrowest the map is drawn before it scrolls: its station lines (text-xs) no
    # smaller than text-2xs, the smallest text the UI draws on its map.
    min_w = w * TEXT["2xs"] / SUB_PX
    cls = "k-map" + (" k-map--bare" if spec.bare else "")
    play = ' data-play=""' if spec.play else ""
    cap = f'<figcaption class="k-map__caption">{_e(spec.caption)}</figcaption>' if spec.caption else ""
    return (
        f'<figure class="{cls}" style="--map-w: {_n(w)}px; --map-min-w: {_n(min_w)}px"{play}>'
        f'<div class="k-map__scroll"><div class="station-map" data-level="{LEVEL}">{svg}</div></div>{cap}</figure>'
    )


def _thread_path(pts: Sequence[Point], kind: str, at: Optional[Tuple[float, float]]) -> str:
    cls = f"station-map__thread-path station-map__thread-path--{kind}"
    if at is None:
        return f'<path d="{path_of(pts)}" class="{cls}"/>'
    start, dur = at
    return (f'<path d="{path_of(pts)}" class="{cls} k-map__draw" pathLength="1" '
            f'style="--map-at: {_n(start)}; --map-for: {_n(dur)}"/>')


def _station(s: Station, reach: Optional[float]) -> str:
    x, y, r = s.x, s.y, R
    parts = []
    if s.state == "held":
        parts.append(f'<circle class="station__ring2" cx="{_n(x)}" cy="{_n(y)}" r="{_n(r + 3)}"/>')
    style = f' style="--map-at: {_n(reach)}"' if reach is not None else ""
    parts.append(f'<circle class="station__dot{" k-map__reach" if reach is not None else ""}" cx="{_n(x)}" cy="{_n(y)}" r="{_n(r)}"{style}/>')
    if s.state == "failed":
        parts.append(f'<path class="station__notch" d="M{_n(x)} {_n(y)} L{_n(x + r * 1.2)} {_n(y - r * 0.7)} L{_n(x + r * 1.2)} {_n(y + r * 0.7)} Z"/>')
    if s.state == "waiting":
        parts.append(f'<path class="station__tick" d="M{_n(x)} {_n(y - r * 0.6)} L{_n(x)} {_n(y)} L{_n(x + r * 0.5)} {_n(y)}"/>')
    if s.state == "promoting":
        parts.append(f'<circle class="station__arc" cx="{_n(x)}" cy="{_n(y)}" r="{_n(r + 3)}" pathLength="100"/>')
    parts.append(f'<text x="{_n(x - r)}" y="{_n(y - r - NAME_RISE)}" class="station__name">{_e(s.name)}</text>')
    for i, (cls, t) in enumerate(s.lines):
        tone = f' data-tone="{s.state}"' if cls == "station__incoming" else ""
        parts.append(f'<text x="{_n(x - r)}" y="{_n(y + r + LINE * (i + 1))}" class="{cls}"{tone}>{_e(t)}</text>')
    return f'<g class="station" data-station="{_e(s.id)}" data-tone="{s.state}">{"".join(parts)}</g>'


def _gate(e: Edge, g: Gate, i: int, clock: Optional["_Clock"]) -> str:
    px, py, dx, dy = point_before_end(e.points, gate_offset(i))
    half = GATE_TICK if g.holding else GATE_TICK / 2
    nx, ny = -dy * half, dx * half
    tone = {"waiting": "waiting", "blocked": "blocked"}.get(g.state, "verified")
    parts = [f'<line class="station-map__gate-bar" x1="{_n(px - nx)}" y1="{_n(py - ny)}" x2="{_n(px + nx)}" y2="{_n(py + ny)}"/>']
    if i == 0 and e.labelled:
        label_y = py - half - GATE_LEADER - 4
        parts.append(f'<line class="station-map__gate-leader" x1="{_n(px)}" y1="{_n(label_y + 3)}" x2="{_n(px)}" y2="{_n(py - half - 1)}"/>')
        parts.append(f'<text x="{_n(px + 3)}" y="{_n(label_y)}" text-anchor="end" class="station-map__gate-label">{_e(_gate_label(e.gates))}</text>')
    if g.approval:
        cx = px - GATE_TICK / 2
        parts.append(
            '<g class="station-map__gate-quorum">'
            f'<circle cx="{_n(cx)}" cy="{_n(py + half + GRID_GAP * 2)}" r="{_n(GRID_GAP)}"/>'
            f'<path d="M{_n(px - GATE_TICK)} {_n(py + half + GATE_TICK)} a{_n(GATE_TICK / 2)} {_n(GATE_TICK / 2)} 0 0 1 {_n(GATE_TICK)} 0"/>'
            f'<text x="{_n(px + GRID_GAP)}" y="{_n(py + half + GATE_TICK)}">{_e(str(g.approval))}</text></g>'
        )
    play = ""
    if clock and g.opens:
        play = f' data-opens="" style="--map-at: {_n(clock.opens(e))}"'
    return f'<g class="station-map__gate" data-tone="{tone}" data-gate="{_e(g.name)}"{play}>{"".join(parts)}</g>'


class _Clock:
    """Play mode: when each part of the thread is drawn, in --k-dur-thread units. The
    version leaves its first station, runs edge by edge, waits at a gate that opens,
    and reaches each station when its edge ends."""

    START = 0.5
    WAIT = 4.0

    def __init__(self, spec: Spec):
        self.spec = spec
        self.at: Dict[str, float] = {}
        self.edges: Dict[int, Tuple[float, float]] = {}
        self.splits: Dict[int, Tuple[Tuple[float, float], Tuple[float, float]]] = {}
        self.gates: Dict[int, float] = {}
        thread = set(spec.thread)
        first = [s for s in spec.thread if not any(e.a in thread for e in spec.into(s))]
        self.lead_at = (0.0, self.START) if spec.lead else None
        for s in first:
            self.at[s] = self.START
        order = sorted(spec.thread, key=lambda s: spec.stations[s].rank)
        for s in order:
            for e in spec.into(s):
                if e.a not in thread or e.a not in self.at:
                    continue
                t = self.at[e.a]
                if any(g.opens for g in e.gates):
                    lift = t + 1 + self.WAIT
                    self.splits[id(e)] = ((t, 1.0), (lift + 0.5, 0.5))
                    self.gates[id(e)] = lift
                    end = lift + 1
                else:
                    self.edges[id(e)] = (t, 1.0)
                    end = t + 1
                self.at[s] = max(self.at.get(s, 0.0), end)

    def lead(self) -> Optional[Tuple[float, float]]:
        return self.lead_at

    def edge(self, e: Edge) -> Tuple[float, float]:
        return self.edges.get(id(e), (0.0, 0.0))

    def edge_split(self, e: Edge) -> Tuple[Tuple[float, float], Tuple[float, float]]:
        return self.splits[id(e)]

    def opens(self, e: Edge) -> float:
        return self.gates[id(e)]

    def reach(self, sid: str) -> float:
        return self.at.get(sid, 0.0)


# ── the MkDocs hook ──────────────────────────────────────────────────────────

FENCE = re.compile(r"^```station-map[ \t]*\n(.*?)\n```[ \t]*$", re.M | re.S)


COMPONENT_FENCE = re.compile(r"^```component-map[ \t]*\n(.*?)\n```[ \t]*$", re.M | re.S)


def _component_map() -> Any:
    """component_map.py, next to this file (a hook is loaded by path, not as a package)."""
    import importlib.util
    import sys
    name = "kardinal_docs_component_map"
    if name not in sys.modules:
        spec = importlib.util.spec_from_file_location(name, os.path.join(os.path.dirname(os.path.abspath(__file__)), "component_map.py"))
        mod = importlib.util.module_from_spec(spec)  # type: ignore[arg-type]
        sys.modules[name] = mod
        spec.loader.exec_module(mod)  # type: ignore[union-attr]
    return sys.modules[name]


def render_components(raw: Any) -> str:
    cm = _component_map()
    m = cm.Metrics(_T, _px, MONO_EM, DISPLAY_EM, R, CHAMFER, RANK_SEP, GATE_TICK)
    try:
        return cm.render(raw, m)
    except cm.SpecError as err:
        raise SpecError(str(err)) from err


def render_markdown(markdown: str, where: str) -> str:
    n = [0]

    def sub(m: "re.Match[str]") -> str:
        n[0] += 1
        try:
            raw = yaml.safe_load(m.group(1))
            return "\n" + render(raw) + "\n"
        except (SpecError, yaml.YAMLError) as err:
            raise SpecError(f"{where}: station map {n[0]}: {err}") from err

    def sub_components(m: "re.Match[str]") -> str:
        n[0] += 1
        try:
            raw = yaml.safe_load(m.group(1))
            return "\n" + render_components(raw) + "\n"
        except (SpecError, yaml.YAMLError) as err:
            raise SpecError(f"{where}: component map {n[0]}: {err}") from err

    return COMPONENT_FENCE.sub(sub_components, FENCE.sub(sub, markdown))


def on_config(config: Any) -> Any:
    with open(UI_CSS, encoding="utf-8") as f:
        css = f.read()
    missing = [c for c in UI_CLASSES if not re.search(r"\." + re.escape(c) + r"(?![\w-])", css)]
    # station__name and others are styled; station-map__nodes and __edges are only groups.
    missing = [c for c in missing if c not in ("station-map__edges", "station-map__nodes", "station-map__gates")]
    if missing:
        from mkdocs.exceptions import PluginError
        raise PluginError(f"web/src/styles/StationMap.css no longer styles {missing}: update hack/docs-station-map")
    return config


def on_files(files: Any, config: Any) -> Any:
    from mkdocs.structure.files import File
    with open(UI_CSS, encoding="utf-8") as f:
        css = f.read()
    header = "/* Generated from web/src/styles/StationMap.css by hack/docs-station-map: do not edit. */\n"
    files.append(File.generated(config, SITE_CSS, content=header + css))
    return files


def on_page_markdown(markdown: str, page: Any, config: Any, files: Any) -> str:
    if "```station-map" not in markdown and "```component-map" not in markdown:
        return markdown
    try:
        return render_markdown(markdown, page.file.src_uri)
    except SpecError as err:
        from mkdocs.exceptions import PluginError
        raise PluginError(str(err)) from err


def on_env(env: Any, config: Any, files: Any) -> Any:
    from markupsafe import Markup

    def station_map(spec: str) -> Markup:
        try:
            return Markup(render(yaml.safe_load(spec)))
        except (SpecError, yaml.YAMLError) as err:
            from mkdocs.exceptions import PluginError
            raise PluginError(f"overrides: station map: {err}") from err

    env.globals["station_map"] = station_map
    return env


def on_serve(server: Any, config: Any, builder: Any) -> Any:
    server.watch(UI_CSS)
    server.watch(os.path.abspath(__file__))
    server.watch(os.path.join(os.path.dirname(os.path.abspath(__file__)), "component_map.py"))
    return server
