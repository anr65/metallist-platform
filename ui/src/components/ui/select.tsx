import * as React from "react"
import { cn } from "cn"
import { CheckIcon, ChevronDownIcon } from "lucide-react"
import { Popover } from "radix-ui"
import { Command, CommandInput, CommandItem, CommandList } from "./command"
import { useDropdownSearch } from "../../dropdown-search"

type Option = { value: string; label: string; children: React.ReactNode; disabled?: boolean }
const SelectContext = React.createContext<any>(null)
function text(node: React.ReactNode): string {
  return React.Children.toArray(node).map(child => React.isValidElement<any>(child) ? text(child.props.children) : String(child)).join('')
}
function optionsFrom(children: React.ReactNode): Option[] {
  return React.Children.toArray(children).flatMap(child => {
    if (!React.isValidElement<any>(child)) return []
    if (child.type === SelectItem) return [{ ...child.props, label: child.props.textValue ?? text(child.props.children) }]
    return optionsFrom(child.props.children)
  })
}
function Select({ children, value, defaultValue = '', onValueChange, name, required, disabled, open: controlledOpen, onOpenChange, searchSource = '', searchContext = '' }: {
  children: React.ReactNode; value?: string; defaultValue?: string; onValueChange?: (value: string) => void;
  name?: string; required?: boolean; disabled?: boolean; open?: boolean; onOpenChange?: (open: boolean) => void;
  searchSource?: string; searchContext?: string;
}) {
  const [internalValue, setValue] = React.useState(defaultValue)
  const [internalOpen, setOpen] = React.useState(false)
  const [query, setQuery] = React.useState('')
  const triggerRef = React.useRef<HTMLButtonElement>(null)
  const nativeRef = React.useRef<HTMLSelectElement>(null)
  const chosen = value ?? internalValue
  const open = controlledOpen ?? internalOpen
  const options = optionsFrom(children)
  const change = (next: string) => { setValue(next); onValueChange?.(next) }
  const changeOpen = (next: boolean) => { setOpen(next); setQuery(''); onOpenChange?.(next) }
  const result = useDropdownSearch(options.map(({ value, label }) => ({ value, label })), query, open, searchSource, searchContext)
  React.useEffect(() => {
    const form = nativeRef.current?.form
    const reset = () => { setValue(defaultValue); onValueChange?.(defaultValue) }
    form?.addEventListener('reset', reset)
    return () => form?.removeEventListener('reset', reset)
  }, [defaultValue, onValueChange])
  return <SelectContext.Provider value={{ chosen, options, change, changeOpen, query, setQuery, result, disabled, triggerRef }}>
    <Popover.Root open={open} onOpenChange={changeOpen}>
      <select ref={nativeRef} className="sr-only" tabIndex={-1} aria-hidden="true" name={name} required={required} disabled={disabled} value={chosen} onChange={event => change(event.target.value)} onInvalid={event => { event.preventDefault(); triggerRef.current?.focus(); changeOpen(true) }}>
        <option value="" />{options.map(option => <option key={option.value} value={option.value} disabled={option.disabled}>{option.label}</option>)}
      </select>
      {children}
    </Popover.Root>
  </SelectContext.Provider>
}
function SelectValue({ placeholder, children }: { placeholder?: string; children?: React.ReactNode }) {
  const { chosen, options } = React.useContext(SelectContext)
  return <span data-slot="select-value" className="min-w-0 truncate">{children ?? options.find((option: Option) => option.value === chosen)?.children ?? placeholder}</span>
}
function SelectTrigger({ className, children, size = 'default', ...props }: React.ComponentProps<'button'> & { size?: 'sm' | 'default' }) {
  const { disabled, triggerRef } = React.useContext(SelectContext)
  return <Popover.Trigger ref={triggerRef} type="button" disabled={disabled} data-slot="select-trigger" className={cn('flex w-full min-w-0 items-center justify-between gap-2 rounded-xl border border-input bg-white/60 px-3.5 py-2 text-sm shadow-xs outline-none focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/30 disabled:opacity-50', size === 'sm' ? 'h-9' : 'h-11', className)} {...props}>{children}<ChevronDownIcon className="size-4 shrink-0 opacity-50" /></Popover.Trigger>
}
function SelectContent({ className, align = 'start' }: { children?: React.ReactNode; className?: string; align?: 'start' | 'center' | 'end'; position?: string }) {
  const { chosen, options, change, changeOpen, query, setQuery, result } = React.useContext(SelectContext)
  const matches = options.filter((option: Option) => result.values.includes(option.value))
  return <Popover.Portal><Popover.Content data-slot="select-content" align={align} sideOffset={4} className={cn('z-50 w-[var(--radix-popover-trigger-width)] min-w-48 max-w-[calc(100vw-2rem)] overflow-hidden rounded-xl border bg-popover text-popover-foreground shadow-xl', className)}>
    <Command shouldFilter={false}>
      <CommandInput aria-label="Поиск в списке" placeholder="Найти…" value={query} onValueChange={setQuery} maxLength={200} />
      <div role="status" aria-live="polite" className="text-sm text-muted-foreground">
        {result.loading && <p className="p-3">Поиск…</p>}
        {result.error && <div className="p-3">{result.error}<button type="button" className="mt-2 block underline" onClick={result.retry}>Повторить поиск</button></div>}
        {!result.loading && !result.error && !matches.length && <p className="p-3">Ничего не найдено.</p>}
      </div>
      <CommandList aria-label="Варианты выбора" className="max-h-[min(300px,var(--radix-popover-content-available-height))]">
        {matches.map((option: Option) => <CommandItem key={option.value} value={option.value} disabled={option.disabled} className="min-h-11 px-3 py-2.5" onSelect={() => { change(option.value); changeOpen(false) }}><span className="min-w-0 flex-1 break-words">{option.children}</span>{chosen === option.value && <CheckIcon className="size-4" />}</CommandItem>)}
      </CommandList>
    </Command>
  </Popover.Content></Popover.Portal>
}
// Declarative options are collected by Select, keeping existing call sites intact.
function SelectItem(_props: { value: string; children: React.ReactNode; disabled?: boolean; className?: string; textValue?: string }) { return null }
function SelectGroup({ children }: { children?: React.ReactNode }) { return <>{children}</> }
function SelectLabel({ children }: { children?: React.ReactNode }) { return <>{children}</> }
function SelectSeparator() { return null }
function SelectScrollUpButton() { return null }
function SelectScrollDownButton() { return null }
export { Select, SelectContent, SelectGroup, SelectItem, SelectLabel, SelectSeparator, SelectScrollUpButton, SelectScrollDownButton, SelectTrigger, SelectValue }
