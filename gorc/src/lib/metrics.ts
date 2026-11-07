import type { Quality, Reading } from './types'

/** Display labels and formatting rules per metric, keyed by the backend's id. */
const META: Record<string, { label: string; digits: number; accent?: string }> = {
  spo2: { label: 'SpO₂', digits: 0, accent: 'cyan' },
  pulse_rate: { label: 'Pulse rate', digits: 0, accent: 'green' },
  perfusion_index: { label: 'Perfusion index', digits: 1 },
  signal_strength: { label: 'Signal', digits: 0 },
  weight: { label: 'Weight', digits: 2, accent: 'violet' },
  height: { label: 'Height', digits: 1, accent: 'violet' },
  bmi: { label: 'BMI', digits: 1 },
  body_fat: { label: 'Body fat', digits: 1 },
  lean_mass: { label: 'Lean mass', digits: 2 },
  temperature: { label: 'Temperature', digits: 2, accent: 'amber' },
  respiration_rate: { label: 'Respiration', digits: 0 },
  bp_systolic: { label: 'Systolic', digits: 0, accent: 'red' },
  bp_diastolic: { label: 'Diastolic', digits: 0, accent: 'red' },
  bp_mean: { label: 'Mean arterial', digits: 0 },
  battery: { label: 'Battery', digits: 0 },
  pleth: { label: 'Plethysmogram', digits: 0 },
}

export function metricLabel(metric: string): string {
  return META[metric]?.label ?? metric.replace(/_/g, ' ')
}

export function metricAccent(metric: string): string {
  return META[metric]?.accent ?? 'slate'
}

export function formatValue(r: Reading): string {
  const digits = META[r.metric]?.digits ?? 1
  return r.value.toFixed(digits)
}

/** Order tiles so the clinically important numbers come first. */
const ORDER = [
  'spo2',
  'pulse_rate',
  'perfusion_index',
  'bp_systolic',
  'bp_diastolic',
  'bp_mean',
  'temperature',
  'respiration_rate',
  'weight',
  'height',
  'bmi',
  'body_fat',
  'lean_mass',
  'signal_strength',
  'battery',
]

export function sortMetrics(readings: Reading[]): Reading[] {
  return [...readings].sort((a, b) => {
    const ia = ORDER.indexOf(a.metric)
    const ib = ORDER.indexOf(b.metric)
    return (ia < 0 ? 99 : ia) - (ib < 0 ? 99 : ib) || a.metric.localeCompare(b.metric)
  })
}

export const QUALITY_LABEL: Record<Quality, string> = {
  ok: '',
  searching: 'searching',
  artifact: 'unstable',
  no_sensor: 'no sensor',
}
