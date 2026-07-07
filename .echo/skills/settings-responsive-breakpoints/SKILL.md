---
name: settings-responsive-breakpoints
description: Responsive breakpoints for the settings panel grid and sidebar layout across desktop, tablet, and mobile viewports.
triggers:
    - settings panel
    - responsive
    - breakpoints
    - mobile
    - tablet
    - grid layout
    - sidebar stacking
    - CSS media queries
---

## Responsive Settings Panel Breakpoints

### Grid Layout Progression
- **Desktop (>1024px)**: `.settings-grid` uses 4 columns (`repeat(4, minmax(0, 1fr))`)
- **Tablet (<=1024px)**: Collapses to 2 columns (`repeat(2, 1fr)`) via `@media (max-width: 1024px)`
- **Mobile (<=720px)**: Collapses to 1 column (`1fr`) via `@media (max-width: 720px)`

### Sidebar Stacking
At <=720px, `.settings-layout` switches from side-by-side (`grid-template-columns: 150px minmax(0, 1fr)`) to stacked (`grid-template-columns: minmax(0, 1fr)`), placing the nav above content. The nav becomes horizontally scrollable tabs.

### Key CSS Rules
```css
/* 1024px - tablet: 2 columns */
@media (max-width: 1024px) {
  .settings-grid { grid-template-columns: repeat(2, 1fr); }
}

/* 920px - removes settings-grid from full collapse list, keeping 2 cols down to 720px */
/* Note: settings-grid was removed from the 920px one-column reset block */

/* 720px - mobile: 1 column + no overflow */
.settings-grid { grid-template-columns: 1fr; }
.field-wide { grid-column: auto; }
.settings-panel { overflow-x: hidden; }
.settings-layout { grid-template-columns: minmax(0, 1fr); }
```

### Common Pitfalls
- Do not add `.settings-grid` back into the 920px collapse selector - it should stay 2-col between 720-1024px.
- `.field-wide` must be reset to `auto` at 720px so wide fields don't span 2 columns in a single-column layout.
- Add `overflow-x: hidden` to `.settings-panel` at mobile widths to prevent accidental horizontal scroll from nested grids.
