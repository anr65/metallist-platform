import * as React from "react"
import { cn } from "cn"
import { formatNumber, rawNumber } from "@/lib/number-input"

function Input({ className, type, numberMask, ...props }: React.ComponentProps<"input"> & { numberMask?: boolean }) {
  const numeric = numberMask ?? (type === 'number' || props.inputMode === 'decimal' || (props.inputMode === 'numeric' && type !== 'password'));
  const hiddenRef = React.useRef<HTMLInputElement>(null);
  const { name, value, defaultValue, onChange, ...rest } = props;
  const controlled = value !== undefined;
  function change(event: React.ChangeEvent<HTMLInputElement>) {
    const input = event.currentTarget;
    const raw = rawNumber(input.value);
    const position = rawNumber(input.value.slice(0, input.selectionStart ?? input.value.length)).length;
    const formatted = formatNumber(raw);
    input.value = raw;
    if (hiddenRef.current) hiddenRef.current.value = raw;
    onChange?.(event);
    input.value = formatted;
    let caret = 0, count = 0;
    while (caret < formatted.length && count < position) {
      if (formatted[caret] !== ' ') count++;
      caret++;
    }
    input.setSelectionRange(caret, caret);
    const number = Number(raw.replace(',', '.'));
    const invalid = raw !== '' && (!/^-?\d+(?:[.,]\d*)?$/.test(raw) || !Number.isFinite(number));
    const outside = raw !== '' && (props.min !== undefined && number < Number(props.min) || props.max !== undefined && number > Number(props.max));
    const step = props.step === 'any' ? null : Number(props.step ?? (type === 'number' ? 1 : 0));
    const base = Number(props.min ?? 0);
    const offStep = step && raw !== '' && Math.abs((number - base) / step - Math.round((number - base) / step)) > 1e-7;
    input.setCustomValidity(invalid ? 'Введите число' : outside ? 'Число вне допустимого диапазона' : offStep ? 'Недопустимый шаг числа' : '');
  }
  return (
    <>
    {numeric && name && <input ref={hiddenRef} type="hidden" name={name} disabled={props.disabled} {...(controlled ? { value: rawNumber(value) } : { defaultValue: rawNumber(defaultValue) })} />}
    <input
      type={numeric ? 'text' : type}
      data-slot="input"
      className={cn(
        "h-11 w-full min-w-0 rounded-xl border border-input bg-white/60 px-3.5 py-1 text-base shadow-xs transition-[color,box-shadow,background] outline-none selection:bg-primary selection:text-primary-foreground file:inline-flex file:h-7 file:border-0 file:bg-transparent file:text-sm file:font-medium file:text-foreground placeholder:text-muted-foreground/70 disabled:pointer-events-none disabled:cursor-not-allowed disabled:opacity-50 md:text-sm dark:bg-input/30",
        "focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50",
        "aria-invalid:border-destructive aria-invalid:ring-destructive/20 dark:aria-invalid:ring-destructive/40",
        className
      )}
      {...rest}
      name={numeric ? undefined : name}
      inputMode={numeric ? (type === 'number' ? 'numeric' : props.inputMode) : props.inputMode}
      value={numeric && controlled ? formatNumber(value) : value}
      defaultValue={numeric && defaultValue !== undefined ? formatNumber(defaultValue) : defaultValue}
      onChange={numeric ? change : onChange}
    />
    </>
  )
}

export { Input }
