import { cn } from "@/lib/utils";
import { useBranding } from "@/hooks/useBranding";

import { DEFAULT_MARK_URL, DEFAULT_WORDMARK_URL } from "@/lib/productBrand";

export type SiloBrandVariant = "wordmark" | "mark";

interface SiloBrandProps {
  className?: string;
  imageClassName?: string;
  variant?: SiloBrandVariant;
}

export function SiloBrand({ className, imageClassName, variant = "wordmark" }: SiloBrandProps) {
  const isMark = variant === "mark";
  const { serverName, wordmarkUrl, markUrl } = useBranding();
  const src = isMark ? (markUrl ?? DEFAULT_MARK_URL) : (wordmarkUrl ?? DEFAULT_WORDMARK_URL);

  return (
    <span className={cn("block shrink-0", !isMark && "overflow-hidden", className)}>
      <img
        src={src}
        alt={serverName}
        className={cn("h-full w-full object-contain", isMark && "rounded-lg", imageClassName)}
      />
    </span>
  );
}
