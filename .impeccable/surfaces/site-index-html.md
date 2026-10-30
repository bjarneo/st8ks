---
version: 1
slug: "site-index-html"
primary_target: "site/index.html"
related_targets: []
---

# Surface brief: st8ks landing page

Scope: `site/index.html`, the GitHub Pages landing page at bjarneo.github.io/st8ks. Mode: Persuade.

Audience and job: Kubernetes engineers on call who must find and fix a broken workload fast. They decide whether st8ks is worth a download.

Action: Download the latest release for the visitor's OS. Secondary: the GitHub repo and the docs.

Proof: real screenshots in `docs/images`, the measured numbers in `docs/architecture.md`, MIT license, no account, no telemetry. No testimonials, logos, user counts or certifications exist. Do not invent them.

Constraints: plain static HTML and CSS, no build step, deployed by `.github/workflows/pages.yml`. ASD-STE100 copy. No personal names. Inherit the app's visual system from `frontend/src/styles.css`.

## Direction contract

THESIS: The page is one unified diff between on-call with kubectl and on-call with st8ks. It refuses the category arrangement of headline, floating screenshot and a grid of icon feature cards.

OWN-WORLD: The app itself. Ground #111214, panels #16171a, hairlines #2a2c31, Geist for prose, Geist Mono only for code, commands, line numbers and measurements. Full-width tinted diff rows in the app's red and green, a line-number gutter, faint `@@` hunk headers, the green dry-run box, blue only on actions. The red stone mark lives in the nav.

STORY: The visitor sees a real failure fixed as a diff and the server dry run pass. Then each hunk replaces a kubectl step with the st8ks view of it. Then the measured speed, the local and free facts, and the download for their OS.

FIRST VIEWPORT: A thin nav. The headline spans the top. Below it, a coded diff panel at near full width with the app's toolbar: Live, Edit and Diff, "+2 -2", Server dry-run, and the Download button in the slot where Apply sits. Lines 50 to 56 of the checkout Deployment show memory 64Mi to 320Mi, and the dry-run box sits above them.

FORM: Structure 2 of 7, "The diff". Seed key afb119fd. Signature interaction: the Live, Edit and Diff control and the Server dry-run button work in the hero.

FINISH: unreviewed and undocumented is unfinished; this build ends with the finish review, the verdict, DESIGN.md, and every shipping raster carrying its provenance
